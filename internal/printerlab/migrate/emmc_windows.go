package migrate

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

type windowsEMMC struct{ backend *nativeBackend }

func platformEMMC(b *nativeBackend) emmcPlatform { return windowsEMMC{b} }

const emmcDiskProperties = `Number,FriendlyName,SerialNumber,UniqueId,@{n='BusType';e={$_.BusType.ToString()}},Size,LogicalSectorSize,IsBoot,IsSystem,IsReadOnly,IsOffline`

func physicalDiskNumber(device string) (int, error) {
	match := regexp.MustCompile(`(?i)^\\\\\.\\PhysicalDrive([0-9]+)$`).FindStringSubmatch(device)
	if len(match) != 2 {
		return 0, fmt.Errorf(`target must be a whole USB disk such as \\.\PhysicalDrive1`)
	}
	return strconv.Atoi(match[1])
}

func (p windowsEMMC) powershell(command string) ([]byte, error) {
	raw, _, err := p.backend.capture("powershell.exe", []string{"-NoProfile", "-NonInteractive", "-Command", "$ErrorActionPreference='Stop'; " + command}, nil)
	return raw, err
}

func (p windowsEMMC) List() ([]EMMCDisk, error) {
	raw, err := p.powershell(`$disks=@(Get-Disk | Where-Object { $_.BusType -eq 'USB' -and -not $_.IsBoot -and -not $_.IsSystem } | Select-Object ` + emmcDiskProperties + `); ConvertTo-Json -InputObject $disks -Compress`)
	if err != nil {
		return nil, err
	}
	var disks []EMMCDisk
	if err := json.Unmarshal(raw, &disks); err != nil {
		return nil, err
	}
	for i := range disks {
		normalizeEMMCDisk(&disks[i])
	}
	return disks, nil
}

func normalizeEMMCDisk(d *EMMCDisk) {
	d.Device = fmt.Sprintf(`\\.\PhysicalDrive%d`, d.Number)
	d.SerialNumber = strings.TrimSpace(d.SerialNumber)
	d.UniqueID = strings.TrimSpace(d.UniqueID)
	// Preserve friendly name for identity checks, sanitize it only when displaying.
}

func (p windowsEMMC) Inspect(device string) (EMMCDisk, error) {
	var disk EMMCDisk
	number, err := physicalDiskNumber(device)
	if err != nil {
		return disk, err
	}
	raw, err := p.powershell(fmt.Sprintf("Get-Disk -Number %d | Select-Object ", number) + emmcDiskProperties + " | ConvertTo-Json -Compress")
	if err != nil {
		return disk, err
	}
	obj, err := decodeObject(raw)
	if err != nil {
		return disk, err
	}
	for _, key := range []string{"Number", "FriendlyName", "SerialNumber", "UniqueId", "BusType", "Size", "LogicalSectorSize", "IsBoot", "IsSystem", "IsReadOnly", "IsOffline"} {
		if _, ok := obj[key]; !ok {
			return disk, fmt.Errorf("disk inspection omitted %s", key)
		}
	}
	for _, key := range []string{"IsBoot", "IsSystem", "IsReadOnly", "IsOffline"} {
		if _, ok := obj[key].(bool); !ok {
			return disk, fmt.Errorf("disk inspection did not prove %s", key)
		}
	}
	if err := json.Unmarshal(raw, &disk); err != nil {
		return disk, err
	}
	if disk.Number != number {
		return disk, fmt.Errorf("disk inspection returned another disk")
	}
	normalizeEMMCDisk(&disk)
	return disk, nil
}

func psLiteral(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }

func (p windowsEMMC) Offline(expected EMMCDisk) error {
	// Recheck identity in the same PowerShell process that offlines the disk.
	command := fmt.Sprintf(`$d=Get-Disk -Number %d
if ($d.BusType -ne 'USB' -or $d.IsBoot -or $d.IsSystem -or $d.IsReadOnly -or $d.Size -ne %d -or $d.LogicalSectorSize -ne 512 -or $d.SerialNumber.Trim() -ne %s -or $d.UniqueId.Trim() -ne %s) { throw 'Disk identity or eligibility changed; no write attempted' }
if (-not ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) { throw 'Run PowerShell as Administrator for the eMMC write' }
Set-Disk -InputObject $d -IsOffline $true
`, expected.Number, expected.Size, psLiteral(expected.SerialNumber), psLiteral(expected.UniqueID))
	if _, err := p.powershell(command); err != nil {
		return fmt.Errorf("cannot offline selected USB disk: %w", err)
	}
	current, err := p.Inspect(expected.Device)
	if err != nil {
		return err
	}
	if !sameEMMCDisk(current, expected) || !current.IsOffline {
		return fmt.Errorf("selected disk was not proven offline")
	}
	return nil
}

func (p windowsEMMC) Open(expected EMMCDisk, write bool) (emmcHandle, error) {
	if _, err := physicalDiskNumber(expected.Device); err != nil {
		return nil, err
	}
	if write {
		current, err := p.Inspect(expected.Device)
		if err != nil {
			return nil, err
		}
		if !sameEMMCDisk(current, expected) || !current.IsOffline {
			return nil, fmt.Errorf("target changed or is no longer offline")
		}
	}
	name, err := syscall.UTF16PtrFromString(expected.Device)
	if err != nil {
		return nil, err
	}
	access := uint32(syscall.GENERIC_READ)
	if write {
		access |= syscall.GENERIC_WRITE
	}
	// Exclusive synchronous handle, write-through. No create/truncate flags.
	handle, err := syscall.CreateFile(name, access, 0, nil, syscall.OPEN_EXISTING, 0x80000000, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), expected.Device)
	if err := verifyEMMCRawHandle(handle, expected); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func deviceControl(handle syscall.Handle, code uint32, input []byte, capacity int) ([]byte, error) {
	output := make([]byte, capacity)
	var in *byte
	if len(input) > 0 {
		in = &input[0]
	}
	var returned uint32
	if err := syscall.DeviceIoControl(handle, code, in, uint32(len(input)), &output[0], uint32(len(output)), &returned, nil); err != nil {
		return nil, err
	}
	if returned > uint32(len(output)) {
		return nil, fmt.Errorf("device query exceeded its buffer")
	}
	return output[:returned], nil
}

func verifyEMMCRawHandle(handle syscall.Handle, expected EMMCDisk) error {
	length, err := deviceControl(handle, 0x7405c, nil, 8) // IOCTL_DISK_GET_LENGTH_INFO
	if err != nil || len(length) != 8 {
		return fmt.Errorf("cannot verify opened raw disk capacity: %v", err)
	}
	if int64(binary.LittleEndian.Uint64(length)) != expected.Size {
		return fmt.Errorf("opened raw disk has a different capacity")
	}
	number, err := deviceControl(handle, 0x2d1080, nil, 12) // IOCTL_STORAGE_GET_DEVICE_NUMBER
	if err != nil || len(number) != 12 {
		return fmt.Errorf("cannot verify opened disk number: %v", err)
	}
	if binary.LittleEndian.Uint32(number[4:8]) != uint32(expected.Number) {
		return fmt.Errorf("opened raw disk is another device")
	}
	descriptor, err := deviceControl(handle, 0x2d1400, make([]byte, 12), 4096) // StorageDeviceProperty, PropertyStandardQuery
	if err != nil || len(descriptor) < 36 {
		return fmt.Errorf("cannot verify opened disk serial number: %v", err)
	}
	return verifyEMMCDescriptor(descriptor, expected)
}

func verifyEMMCDescriptor(descriptor []byte, expected EMMCDisk) error {
	if len(descriptor) < 36 {
		return fmt.Errorf("opened disk descriptor is truncated")
	}
	size := binary.LittleEndian.Uint32(descriptor[4:8])
	if size < 36 || size > uint32(len(descriptor)) {
		return fmt.Errorf("opened disk descriptor size is invalid")
	}
	descriptor = descriptor[:size]
	if binary.LittleEndian.Uint32(descriptor[28:32]) != 7 {
		return fmt.Errorf("opened raw disk is not USB")
	}
	offset := binary.LittleEndian.Uint32(descriptor[24:28])
	if offset < 36 || offset >= uint32(len(descriptor)) {
		return fmt.Errorf("opened raw disk has no serial number")
	}
	end := strings.IndexByte(string(descriptor[offset:]), 0)
	if end < 0 {
		return fmt.Errorf("opened raw disk serial is truncated")
	}
	serial := string(descriptor[offset : offset+uint32(end)])
	if strings.TrimSpace(serial) != expected.SerialNumber {
		return fmt.Errorf("opened raw disk serial differs from the selected target")
	}
	return nil
}

package migrate

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const factoryEMMCSize = int64(31037849600)
const factoryPrefixSize = int64(16 * 1024 * 1024)

type emmcPartition struct {
	Index   int    `json:"index"`
	Type    byte   `json:"type"`
	Start   uint32 `json:"start_lba"`
	Sectors uint32 `json:"sectors"`
}

func factoryLayout(sector []byte) ([]emmcPartition, error) {
	if len(sector) != 512 || sector[510] != 0x55 || sector[511] != 0xaa {
		return nil, fmt.Errorf("source does not contain a valid MBR signature")
	}
	parts := []emmcPartition{}
	for i := 0; i < 4; i++ {
		entry := sector[446+i*16 : 462+i*16]
		part := emmcPartition{i + 1, entry[4], binary.LittleEndian.Uint32(entry[8:12]), binary.LittleEndian.Uint32(entry[12:16])}
		if part.Type != 0 || part.Start != 0 || part.Sectors != 0 {
			parts = append(parts, part)
		}
	}
	expected := []emmcPartition{{1, 0x0e, 32768, 524288}, {2, 0x83, 557056, 59441152}}
	if len(parts) != 2 || parts[0] != expected[0] || parts[1] != expected[1] {
		return nil, fmt.Errorf("source does not match the verified 32 GB QIDI factory layout")
	}
	return parts, nil
}
func (b *nativeBackend) inspectFactorySource(device string) (map[string]any, error) {
	if runtime.GOOS == "windows" {
		match := regexp.MustCompile(`(?i)^\\\\\.\\PhysicalDrive([0-9]+)$`).FindStringSubmatch(device)
		if len(match) != 2 {
			return nil, fmt.Errorf(`device must look like \\.\PhysicalDrive1`)
		}
		number, err := strconv.Atoi(match[1])
		if err != nil {
			return nil, err
		}
		command := fmt.Sprintf("Get-Disk -Number %d -ErrorAction Stop | Select-Object Number,FriendlyName,BusType,PartitionStyle,Size,IsReadOnly,IsOffline | ConvertTo-Json -Compress", number)
		raw, _, err := b.capture("powershell.exe", []string{"-NoProfile", "-NonInteractive", "-Command", command}, nil)
		if err != nil {
			return nil, err
		}
		info, err := decodeObject(raw)
		if err != nil {
			return nil, err
		}
		if info["BusType"] != "USB" || info["PartitionStyle"] != "MBR" || info["Size"] != float64(factoryEMMCSize) || info["IsReadOnly"] != true {
			return nil, fmt.Errorf("source must be a read-only USB MBR disk of exactly %d bytes", factoryEMMCSize)
		}
		return info, nil
	}
	info, err := os.Stat(device)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeDevice == 0 || info.Mode()&os.ModeCharDevice != 0 {
		return nil, fmt.Errorf("factory source must be a block device")
	}
	raw, _, err := b.capture("blockdev", []string{"--getsize64", device}, nil)
	if err != nil {
		return nil, err
	}
	size, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil {
		return nil, err
	}
	raw, _, err = b.capture("blockdev", []string{"--getro", device}, nil)
	if err != nil {
		return nil, err
	}
	if size != factoryEMMCSize || strings.TrimSpace(string(raw)) != "1" {
		return nil, fmt.Errorf("source block device must be read-only and exactly %d bytes", factoryEMMCSize)
	}
	return map[string]any{"device": device, "size": size, "read_only": true}, nil
}
func (b *nativeBackend) freeDiskSpace(directory string) (int64, error) {
	if runtime.GOOS == "windows" {
		drive := strings.TrimSuffix(filepath.VolumeName(directory), ":")
		if !regexp.MustCompile(`^[A-Za-z]$`).MatchString(drive) {
			return 0, fmt.Errorf("factory image output must be on a local drive")
		}
		raw, _, err := b.capture("powershell.exe", []string{"-NoProfile", "-NonInteractive", "-Command", "(Get-Volume -DriveLetter " + drive + " -ErrorAction Stop).SizeRemaining"}, nil)
		if err != nil {
			return 0, err
		}
		return strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	}
	raw, _, err := b.capture("df", []string{"-Pk", directory}, nil)
	if err != nil {
		return 0, err
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) < 4 {
		return 0, fmt.Errorf("could not inspect available disk space")
	}
	blocks, err := strconv.ParseInt(fields[3], 10, 64)
	return blocks * 1024, err
}
func (b *nativeBackend) imageFactoryEMMC() error {
	if err := b.confirm("IMAGE-FACTORY-EMMC"); err != nil {
		return err
	}
	device, err := b.required("device")
	if err != nil {
		return err
	}
	output, err := b.required("output")
	if err != nil {
		return err
	}
	output, err = localPath(output)
	if err != nil {
		return err
	}
	info, err := b.inspectFactorySource(device)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(output, 0700); err != nil {
		return err
	}
	free, err := b.freeDiskSpace(output)
	if err != nil {
		return err
	}
	if free < factoryEMMCSize+factoryPrefixSize+1024*1024*1024 {
		return fmt.Errorf("insufficient free space for factory image and readback verification")
	}
	source, err := os.Open(device)
	if err != nil {
		return err
	}
	defer source.Close()
	return b.copyFactoryImage(source, output, info, factoryEMMCSize, factoryPrefixSize)
}

// copyFactoryImage accepts sizes privately for a bounded, real streaming test.
// The public command always uses the qualified factory disk size.
func (b *nativeBackend) copyFactoryImage(source io.Reader, output string, info map[string]any, size, prefixSize int64) error {
	names := []string{"factory-emmc-user-area.img", "emmc-prefix-16MiB.bin", "manifest.json"}
	for _, name := range names {
		for _, suffix := range []string{"", ".partial"} {
			if _, err := os.Lstat(filepath.Join(output, name+suffix)); err == nil {
				return fmt.Errorf("refusing to overwrite existing evidence: %s", name+suffix)
			} else if !os.IsNotExist(err) {
				return err
			}
		}
	}
	imagePath := filepath.Join(output, names[0]+".partial")
	image, err := os.OpenFile(imagePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer image.Close()
	owned := []string{imagePath}
	complete := false
	published := []string{}
	defer func() {
		_ = image.Close()
		for _, path := range owned {
			os.Remove(path)
		}
		if !complete {
			for _, path := range published {
				os.Remove(path)
			}
		}
	}()
	prefixPath := filepath.Join(output, names[1]+".partial")
	prefix, err := os.OpenFile(prefixPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer prefix.Close()
	owned = append(owned, prefixPath)
	hash := sha256.New()
	buffer := make([]byte, 8*1024*1024)
	var copied int64
	nextProgress := int64(1024 * 1024 * 1024)
	started := time.Now()
	var partitions []emmcPartition
	for copied < size {
		if err := b.ctx.Err(); err != nil {
			return err
		}
		count := int64(len(buffer))
		if size-copied < count {
			count = size - copied
		}
		n, err := io.ReadFull(source, buffer[:count])
		if err != nil {
			return fmt.Errorf("unexpected end of source after %d bytes: %w", copied, err)
		}
		raw := buffer[:n]
		if copied == 0 {
			if len(raw) < 512 {
				return fmt.Errorf("source is shorter than MBR")
			}
			partitions, err = factoryLayout(raw[:512])
			if err != nil {
				return err
			}
		}
		if _, err := image.Write(raw); err != nil {
			return err
		}
		if copied < prefixSize {
			length := int64(len(raw))
			if prefixSize-copied < length {
				length = prefixSize - copied
			}
			if _, err := prefix.Write(raw[:length]); err != nil {
				return err
			}
		}
		hash.Write(raw)
		copied += int64(n)
		if copied >= nextProgress || copied == size {
			fmt.Fprintf(b.out, "Copied %.1f GiB (%.1f%%), %.1f MiB/s\n", float64(copied)/(1<<30), 100*float64(copied)/float64(size), float64(copied)/(1<<20)/time.Since(started).Seconds())
			nextProgress += 1 << 30
		}
	}
	if err := image.Sync(); err != nil {
		return err
	}
	if err := prefix.Sync(); err != nil {
		return err
	}
	if err := image.Close(); err != nil {
		return err
	}
	if err := prefix.Close(); err != nil {
		return err
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	readback, err := hashFile(imagePath)
	if err != nil {
		return err
	}
	if readback != digest {
		return fmt.Errorf("factory image readback SHA-256 mismatch")
	}
	prefixSHA, err := hashFile(prefixPath)
	if err != nil {
		return err
	}
	record := map[string]any{"created_utc": utcStamp(), "source": info, "layout": map[string]any{"sector_size": 512, "size": size, "partitions": partitions}, "artifacts": map[string]any{names[0]: map[string]any{"size": size, "sha256": digest}, names[1]: map[string]any{"size": prefixSize, "sha256": prefixSHA}}}
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	manifest := filepath.Join(output, names[2]+".partial")
	if err := exclusiveWrite(manifest, append(raw, '\n')); err != nil {
		return err
	}
	owned = append(owned, manifest)
	for _, index := range []int{2, 1, 0} {
		partial := filepath.Join(output, names[index]+".partial")
		final := filepath.Join(output, names[index])
		if err := publishExclusive(partial, final); err != nil {
			return err
		}
		published = append(published, final)
	}
	complete = true
	fmt.Fprintf(b.out, "Factory image verified: %s\nSHA-256 %s\n", filepath.Join(output, names[0]), digest)
	return nil
}

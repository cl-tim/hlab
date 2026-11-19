package migrate

import (
	"encoding/binary"
	"testing"
)

func TestEMMCWindowsAcceptsOnlyWholePhysicalDiskNames(t *testing.T) {
	for _, device := range []string{`C:\`, `E:`, `\\.\PhysicalDrive3\partition`, `\\.\PhysicalDrive-1`, `\\.\PhysicalDrive1;anything`, `fixture.img`} {
		if _, err := physicalDiskNumber(device); err == nil {
			t.Fatalf("accepted %q", device)
		}
	}
	if number, err := physicalDiskNumber(`\\.\PhysicalDrive3`); err != nil || number != 3 {
		t.Fatalf("disk number %d: %v", number, err)
	}
}

func TestEMMCWindowsRawDescriptorMustProveUSBSerial(t *testing.T) {
	valid := make([]byte, 64)
	binary.LittleEndian.PutUint32(valid[4:8], uint32(len(valid)))
	binary.LittleEndian.PutUint32(valid[24:28], 36)
	binary.LittleEndian.PutUint32(valid[28:32], 7)
	copy(valid[36:], "FIXTURE-ONLY\x00")
	disk := EMMCDisk{SerialNumber: "FIXTURE-ONLY"}
	if err := verifyEMMCDescriptor(valid, disk); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"truncated", "oversized", "non USB", "missing serial", "header serial", "other serial", "unterminated"} {
		t.Run(name, func(t *testing.T) {
			raw := append([]byte(nil), valid...)
			switch name {
			case "truncated":
				raw = raw[:35]
			case "oversized":
				binary.LittleEndian.PutUint32(raw[4:8], 4096)
			case "non USB":
				binary.LittleEndian.PutUint32(raw[28:32], 3)
			case "missing serial":
				binary.LittleEndian.PutUint32(raw[24:28], 0)
			case "header serial":
				binary.LittleEndian.PutUint32(raw[24:28], 8)
			case "other serial":
				raw[36] = 'X'
			case "unterminated":
				for i := 36; i < len(raw); i++ {
					raw[i] = 'X'
				}
			}
			if err := verifyEMMCDescriptor(raw, disk); err == nil {
				t.Fatal("unproven descriptor accepted")
			}
		})
	}
}

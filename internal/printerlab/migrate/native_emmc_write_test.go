package migrate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ulikunitz/xz"
)

// Only this private fixture platform accepts regular files. Production accepts
// whole Windows USB disks and validates their opened device handles.
type fixtureEMMC struct {
	disk                        EMMCDisk
	path                        string
	operations                  []string
	corruptReadback, shortWrite bool
}

func (p *fixtureEMMC) List() ([]EMMCDisk, error)        { return []EMMCDisk{p.disk}, nil }
func (p *fixtureEMMC) Inspect(string) (EMMCDisk, error) { return p.disk, nil }
func (p *fixtureEMMC) Offline(EMMCDisk) error {
	p.operations = append(p.operations, "offline")
	p.disk.IsOffline = true
	return nil
}
func (p *fixtureEMMC) Open(_ EMMCDisk, write bool) (emmcHandle, error) {
	mode := os.O_RDONLY
	operation := "open-readback"
	if write {
		mode, operation = os.O_RDWR, "open-write"
	}
	p.operations = append(p.operations, operation)
	f, err := os.OpenFile(p.path, mode, 0)
	if err != nil {
		return nil, err
	}
	return &fixtureEMMCHandle{File: f, platform: p, writing: write}, nil
}

type fixtureEMMCHandle struct {
	*os.File
	platform *fixtureEMMC
	writing  bool
}

func (f *fixtureEMMCHandle) Write(raw []byte) (int, error) {
	if f.platform.shortWrite {
		return len(raw) - 1, nil
	}
	return f.File.Write(raw)
}
func (f *fixtureEMMCHandle) Read(raw []byte) (int, error) {
	n, err := f.File.Read(raw)
	if f.platform.corruptReadback && n > 0 {
		raw[0] ^= 1
	}
	return n, err
}
func (f *fixtureEMMCHandle) Sync() error {
	f.platform.operations = append(f.platform.operations, "sync")
	return f.File.Sync()
}
func (f *fixtureEMMCHandle) Close() error {
	if f.writing {
		f.platform.operations = append(f.platform.operations, "close-write")
	}
	return f.File.Close()
}

func emmcFixture(t *testing.T) (*nativeBackend, *fixtureEMMC, string, []byte) {
	t.Helper()
	b := nativeTestBackend(t)
	p := &fixtureEMMC{disk: EMMCDisk{Device: `\\.\PhysicalDrive3`, Number: 3, FriendlyName: "Fixture USB eMMC", SerialNumber: "FIXTURE-ONLY", UniqueID: "fixture-id", BusType: "USB", Size: 8192, LogicalSectorSize: 512}, path: filepath.Join(t.TempDir(), "fixture-disk")}
	if err := os.WriteFile(p.path, bytes.Repeat([]byte{0xcc}, int(p.disk.Size)), 0600); err != nil {
		t.Fatal(err)
	}
	b.emmc = p
	raw := bytes.Repeat([]byte{0x24}, 4096)
	raw[510], raw[511] = 0x55, 0xaa
	image := filepath.Join(t.TempDir(), "Armbian_qidi-x6_trixie_current_6.18.54_minimal.img.xz")
	writeEMMCFixtureImage(t, image, raw)
	return b, p, image, raw
}
func writeEMMCFixtureImage(t *testing.T, image string, raw []byte) {
	t.Helper()
	var compressed bytes.Buffer
	w, err := xz.NewWriter(&compressed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	publishEMMCFixtureImage(t, image, compressed.Bytes())
}
func publishEMMCFixtureImage(t *testing.T, image string, compressed []byte) {
	t.Helper()
	hash := sha256.Sum256(compressed)
	digest := hex.EncodeToString(hash[:])
	files := map[string][]byte{image: compressed, image + ".sha": []byte(digest + "  " + filepath.Base(image) + "\n")}
	files[image+".json"], _ = json.Marshal(map[string]string{"url": "https://example.test/" + filepath.Base(image), "filename": filepath.Base(image), "kernel": "6.18.54", "sha256": digest})
	for path, data := range files {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
func planEMMCFixture(t *testing.T, b *nativeBackend, p *fixtureEMMC, image string) EMMCWritePlan {
	t.Helper()
	b.out.(*bytes.Buffer).Reset()
	if err := b.execute([]string{"emmc-plan", "--image", image, "--device", p.disk.Device}); err != nil {
		t.Fatal(err)
	}
	var plan EMMCWritePlan
	if err := json.Unmarshal(b.out.(*bytes.Buffer).Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if len(p.operations) != 0 {
		t.Fatalf("planning modified disk: %v", p.operations)
	}
	return plan
}
func writeEMMCFixture(b *nativeBackend, plan EMMCWritePlan) error {
	return b.execute([]string{"write-emmc", "--plan", plan.Manifest, "--confirm", "WRITE-EMMC"})
}

func TestEMMCWritesVerifiedImageAndReadsBackBeforeReceipt(t *testing.T) {
	b, p, image, raw := emmcFixture(t)
	plan := planEMMCFixture(t, b, p, image)
	if err := writeEMMCFixture(b, plan); err != nil {
		t.Fatal(err)
	}
	disk, err := os.ReadFile(p.path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(disk[:len(raw)], raw) || !bytes.Equal(disk[len(raw):], bytes.Repeat([]byte{0xcc}, len(disk)-len(raw))) {
		t.Fatal("wrong disk contents")
	}
	if strings.Join(p.operations, ",") != "offline,open-write,sync,close-write,open-readback" {
		t.Fatalf("operations: %v", p.operations)
	}
	receipt, err := os.ReadFile(filepath.Join(filepath.Dir(plan.Manifest), "receipt.json"))
	if err != nil || !bytes.Contains(receipt, []byte(plan.RawSHA256)) {
		t.Fatalf("missing verification receipt: %v", err)
	}
	if _, err := os.Stat(plan.RawImage); !os.IsNotExist(err) {
		t.Fatal("successful write retained raw staging image")
	}
}

func TestEMMCRejectsInvalidInputsWithoutOfflining(t *testing.T) {
	cases := map[string]func(*testing.T, *fixtureEMMC, string, []byte){
		"boot disk":         func(_ *testing.T, p *fixtureEMMC, _ string, _ []byte) { p.disk.IsBoot = true },
		"system disk":       func(_ *testing.T, p *fixtureEMMC, _ string, _ []byte) { p.disk.IsSystem = true },
		"read-only disk":    func(_ *testing.T, p *fixtureEMMC, _ string, _ []byte) { p.disk.IsReadOnly = true },
		"non USB":           func(_ *testing.T, p *fixtureEMMC, _ string, _ []byte) { p.disk.BusType = "SATA" },
		"unknown serial":    func(_ *testing.T, p *fixtureEMMC, _ string, _ []byte) { p.disk.SerialNumber = "" },
		"unknown unique ID": func(_ *testing.T, p *fixtureEMMC, _ string, _ []byte) { p.disk.UniqueID = "" },
		"4K sectors":        func(_ *testing.T, p *fixtureEMMC, _ string, _ []byte) { p.disk.LogicalSectorSize = 4096 },
		"too small":         func(_ *testing.T, p *fixtureEMMC, _ string, _ []byte) { p.disk.Size = 512 },
		"wrong compressed hash": func(t *testing.T, _ *fixtureEMMC, image string, _ []byte) {
			os.WriteFile(image+".sha", []byte(strings.Repeat("0", 64)), 0600)
		},
		"missing receipt": func(t *testing.T, _ *fixtureEMMC, image string, _ []byte) {
			if err := os.Remove(image + ".json"); err != nil {
				t.Fatal(err)
			}
		},
		"invalid MBR": func(t *testing.T, _ *fixtureEMMC, image string, raw []byte) {
			raw[510] = 0
			writeEMMCFixtureImage(t, image, raw)
		},
		"unaligned image": func(t *testing.T, _ *fixtureEMMC, image string, raw []byte) {
			writeEMMCFixtureImage(t, image, raw[:len(raw)-1])
		},
		"corrupt xz": func(t *testing.T, _ *fixtureEMMC, image string, _ []byte) {
			compressed, _ := os.ReadFile(image)
			compressed[len(compressed)/2] ^= 1
			publishEMMCFixtureImage(t, image, compressed)
		},
		"truncated xz": func(t *testing.T, _ *fixtureEMMC, image string, _ []byte) {
			compressed, _ := os.ReadFile(image)
			publishEMMCFixtureImage(t, image, compressed[:len(compressed)-10])
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			b, p, image, raw := emmcFixture(t)
			change(t, p, image, raw)
			if err := b.execute([]string{"emmc-plan", "--image", image, "--device", p.disk.Device}); err == nil {
				t.Fatal("invalid image or disk accepted")
			}
			if len(p.operations) != 0 {
				t.Fatalf("modified invalid target: %v", p.operations)
			}
		})
	}
}

func TestEMMCApprovalCannotOutliveImageOrDiskIdentity(t *testing.T) {
	for _, name := range []string{"changed raw image", "changed disk", "cancelled", "missing private gate"} {
		t.Run(name, func(t *testing.T) {
			b, p, image, _ := emmcFixture(t)
			plan := planEMMCFixture(t, b, p, image)
			switch name {
			case "changed raw image":
				f, err := os.OpenFile(plan.RawImage, os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				f.WriteAt([]byte{0xff}, 100)
				f.Close()
			case "changed disk":
				p.disk.SerialNumber = "ANOTHER-DISK"
			case "cancelled":
				ctx, cancel := context.WithCancel(b.ctx)
				cancel()
				b.ctx = ctx
			}
			var err error
			if name == "missing private gate" {
				err = b.execute([]string{"write-emmc", "--plan", plan.Manifest})
			} else {
				err = writeEMMCFixture(b, plan)
			}
			if err == nil || len(p.operations) != 0 {
				t.Fatalf("unsafe write: %v; operations %v", err, p.operations)
			}
		})
	}
}

func TestEMMCFailedWriteOrReadbackRetainsPlanWithoutReceipt(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(fmt.Sprintf("readback=%t", corrupt), func(t *testing.T) {
			b, p, image, _ := emmcFixture(t)
			plan := planEMMCFixture(t, b, p, image)
			p.corruptReadback, p.shortWrite = corrupt, !corrupt
			if err := writeEMMCFixture(b, plan); err == nil {
				t.Fatal("failed write was reported as success")
			}
			if _, err := os.Stat(plan.RawImage); err != nil {
				t.Fatal("failed write removed staged recovery image")
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(plan.Manifest), "receipt.json")); !os.IsNotExist(err) {
				t.Fatal("failed write has a success receipt")
			}
			if strings.Count(strings.Join(p.operations, ","), "open-write") != 1 {
				t.Fatal("write was retried")
			}
		})
	}
}

func TestEMMCCleanupCannotRemoveUnrelatedDirectory(t *testing.T) {
	b, p, image, _ := emmcFixture(t)
	plan := planEMMCFixture(t, b, p, image)
	unrelated := t.TempDir()
	marker := filepath.Join(unrelated, "keep")
	if err := os.WriteFile(marker, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := b.execute([]string{"cleanup-emmc-plan", "--plan", filepath.Join(unrelated, "plan.json")}); err == nil {
		t.Fatal("unrelated cleanup accepted")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("unrelated directory changed")
	}
	if err := b.execute([]string{"cleanup-emmc-plan", "--plan", plan.Manifest}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(plan.Manifest)); !os.IsNotExist(err) {
		t.Fatal("declined plan retained")
	}
}

func TestEMMCExactCopyRejectsShortInput(t *testing.T) {
	b := nativeTestBackend(t)
	if _, err := b.copyEMMCStream(io.Discard, strings.NewReader("short"), 512); err != io.ErrUnexpectedEOF {
		t.Fatalf("short copy: %v", err)
	}
}

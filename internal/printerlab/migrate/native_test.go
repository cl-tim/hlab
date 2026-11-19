package migrate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cl-tim/hlab/internal/printerlab/migration"
)

func nativeTestBackend(t *testing.T) *nativeBackend {
	t.Helper()
	home := t.TempDir()
	root, err := migration.Ensure(home)
	if err != nil {
		t.Fatal(err)
	}
	return &nativeBackend{ctx: context.Background(), root: root, home: home, out: &bytes.Buffer{}, errOut: &bytes.Buffer{}}
}
func TestNativeProbesMatchReviewedReference(t *testing.T) {
	var fixture struct {
		Smoke string                    `json:"smoke_sha256"`
		Bases map[string]map[string]any `json:"bases"`
		Cases []struct {
			Name, Base, Path string
			Remove           bool
			Value            any
			Statuses         []string
		} `json:"cases"`
	}
	raw, err := os.ReadFile("testdata/probe-reference.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, test := range fixture.Cases {
		t.Run(test.Name, func(t *testing.T) {
			raw, _ := json.Marshal(fixture.Bases[test.Base])
			var facts map[string]any
			json.Unmarshal(raw, &facts)
			if test.Path == "" {
				facts = map[string]any{}
			} else {
				parts := strings.Split(test.Path, ".")
				parent := facts
				for _, part := range parts[:len(parts)-1] {
					parent = parent[part].(map[string]any)
				}
				key := parts[len(parts)-1]
				if test.Remove {
					delete(parent, key)
				} else {
					parent[key] = test.Value
				}
			}
			for i, def := range nativeProbeDefinitions {
				got := evaluateNativeProbe(def, facts, fixture.Smoke)
				if got.Status != test.Statuses[i] {
					t.Errorf("%s status %s, want %s (%s)", def.ID, got.Status, test.Statuses[i], got.Detail)
				}
			}
		})
	}
}
func TestNativeRendererPreservesOwnershipAndCommissioning(t *testing.T) {
	b := nativeTestBackend(t)
	dest := filepath.Join(t.TempDir(), "rendered")
	args := []string{"render-config", "--beacon-serial", "/dev/serial/by-id/usb-Beacon_TEST", "--output", dest, "--commissioning"}
	if err := b.execute(args); err != nil {
		t.Fatal(err)
	}
	entries, _ := filepath.Glob(filepath.Join(dest, "*.cfg"))
	if len(entries) != 10 {
		t.Fatalf("got %d configs", len(entries))
	}
	raw, _ := os.ReadFile(filepath.Join(dest, "beacon.cfg"))
	if !bytes.Contains(raw, []byte("accel_axes_map: x, y, z")) || bytes.Contains(raw, []byte("CHANGE_ME")) {
		t.Fatalf("unexpected beacon config")
	}
	if !managedRender(dest) {
		t.Fatal("render has no recognized ownership marker")
	}
	if err := os.WriteFile(filepath.Join(dest, "old-calibration.cfg"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := b.execute(append(args, "--force")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "old-calibration.cfg")); !os.IsNotExist(err) {
		t.Fatal("old config survived replacement")
	}
	if err := os.Remove(filepath.Join(dest, "RENDER-METADATA.json")); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dest, "printer.cfg"))
	if err := b.execute(append(args, "--force")); err == nil {
		t.Fatal("unmanaged replacement accepted")
	}
	after, _ := os.ReadFile(filepath.Join(dest, "printer.cfg"))
	if !bytes.Equal(before, after) {
		t.Fatal("unmanaged output changed")
	}
}
func TestNativeRendererRejectsSourceAndSymlink(t *testing.T) {
	b := nativeTestBackend(t)
	source := filepath.Join(b.root, "mainline-configs", "mainline-klipper-beacon")
	args := []string{"render-config", "--beacon-serial", "/dev/serial/by-id/usb-Beacon_TEST", "--output", source, "--force"}
	if err := b.execute(args); err == nil {
		t.Fatal("source replacement accepted")
	}
	dest := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(source, dest); err != nil {
		t.Skip("symlinks unavailable")
	}
	args[len(args)-2] = dest
	if err := b.execute(args); err == nil {
		t.Fatal("symlink replacement accepted")
	}
}
func validMCUImage(kind string) []byte {
	raw := make([]byte, 128)
	base := uint32(0x08002000)
	stack := uint32(0x20001000)
	if kind == "mainboard" {
		base = 0x08008000
	}
	if kind == "toolhead-katapult" {
		base = 0x08000000
	}
	binary.LittleEndian.PutUint32(raw, stack)
	binary.LittleEndian.PutUint32(raw[4:], base+9)
	return raw
}
func TestNativeImageValidationBounds(t *testing.T) {
	b := nativeTestBackend(t)
	for _, kind := range []string{"mainboard", "toolhead-app", "toolhead-katapult"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "image.bin")
			raw := validMCUImage(kind)
			os.WriteFile(path, raw, 0600)
			if err := b.validateImage(kind, path); err != nil {
				t.Fatal(err)
			}
			for _, corrupt := range []func([]byte) []byte{func(raw []byte) []byte { return raw[:7] }, func(raw []byte) []byte { binary.LittleEndian.PutUint32(raw, 0x10000000); return raw }, func(raw []byte) []byte { raw[4] &= 0xfe; return raw }, func(raw []byte) []byte { binary.LittleEndian.PutUint32(raw[4:], 0x08040001); return raw }, func(raw []byte) []byte { return append(raw, make([]byte, 256*1024)...) }} {
				os.WriteFile(path, corrupt(validMCUImage(kind)), 0600)
				if err := b.validateImage(kind, path); err == nil {
					t.Fatal("invalid vector or size accepted")
				}
			}
		})
	}
}
func writeTestArtifacts(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	manifest := []string{"klipper_commit=" + KlipperRef, "katapult_commit=" + KatapultRef}
	for kind, name := range imageKinds {
		raw := validMCUImage(kind)
		os.WriteFile(filepath.Join(dir, name), raw, 0600)
		manifest = append(manifest, hashBytes(raw)+"  "+name)
	}
	os.WriteFile(filepath.Join(dir, "BUILD-METADATA.txt"), []byte(strings.Join(manifest, "\n")+"\n"), 0600)
	return dir
}
func TestNativeArtifactManifestAndSelection(t *testing.T) {
	dir := writeTestArtifacts(t)
	if _, err := verifyBuildMetadata(dir); err != nil {
		t.Fatal(err)
	}
	b := nativeTestBackend(t)
	b.values = map[string]string{"image": filepath.Join(t.TempDir(), MainboardImage), "artifacts": dir}
	os.WriteFile(b.value("image"), validMCUImage("mainboard"), 0600)
	if _, err := b.verifiedImage("mainboard"); err == nil {
		t.Fatal("image outside artifact set accepted")
	}
	os.WriteFile(filepath.Join(dir, MainboardImage), []byte("changed"), 0600)
	if _, err := verifyBuildMetadata(dir); err == nil {
		t.Fatal("changed artifact accepted")
	}
	dir = writeTestArtifacts(t)
	file := filepath.Join(dir, "BUILD-METADATA.txt")
	raw, _ := os.ReadFile(file)
	os.WriteFile(file, append(raw, []byte("klipper_commit="+KlipperRef+"\n")...), 0600)
	if _, err := verifyBuildMetadata(dir); err == nil {
		t.Fatal("duplicate pin accepted")
	}
}
func TestNativeSDCardStagingVerifiesImage(t *testing.T) {
	b := nativeTestBackend(t)
	dir := writeTestArtifacts(t)
	dest := t.TempDir()
	args := []string{"stage-mainboard-sdcard", "--artifacts", dir, "--image", filepath.Join(dir, MainboardImage), "--output", dest}
	if err := b.execute(args); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dest, "qd_mcu.bin"))
	if !bytes.Equal(raw, validMCUImage("mainboard")) {
		t.Fatal("staged image changed")
	}
	if err := b.execute(args); err == nil {
		t.Fatal("existing staged image overwritten")
	}
}
func TestNativeFlashRequiresVerifiedRecoveryDump(t *testing.T) {
	for _, length := range []int{65535, 65536} {
		t.Run(fmt.Sprint(length), func(t *testing.T) {
			b := nativeTestBackend(t)
			dir := writeTestArtifacts(t)
			flashed := false
			dump := bytes.Repeat([]byte{0x42}, length)
			b.transport = func(command string, input []byte) ([]byte, int, error) {
				if strings.Contains(command, "cat ") {
					return dump, 0, nil
				}
				if strings.Contains(command, "'flash-toolhead-katapult'") {
					flashed = true
					if !strings.Contains(command, hashBytes(dump)) {
						t.Error("erase omitted recovery digest")
					}
					files, _ := filepath.Glob(filepath.Join(b.home, "backups", "*.bin"))
					if len(files) != 1 {
						t.Error("erase before local backup publication")
					}
				}
				return nil, 0, nil
			}
			err := b.execute([]string{"flash-toolhead-katapult", "--printer", "plus4.invalid", "--image", filepath.Join(dir, ToolheadKatapultImage), "--artifacts", dir, "--confirm", "ERASE-TOOLHEAD"})
			if (length == 65536) != (err == nil) || flashed != (length == 65536) {
				t.Fatalf("length %d, flashed=%v err=%v", length, flashed, err)
			}
		})
	}
	b := nativeTestBackend(t)
	calls := 0
	b.transport = func(string, []byte) ([]byte, int, error) { calls++; return nil, 0, nil }
	if err := b.execute([]string{"flash-toolhead-katapult", "--printer", "plus4.invalid", "--image", "missing"}); err == nil || calls != 0 {
		t.Fatal("flash touched printer without confirmation")
	}
}
func TestNativeProbesUnreachableAndBadConnection(t *testing.T) {
	b := nativeTestBackend(t)
	b.transport = func(string, []byte) ([]byte, int, error) { return nil, 255, fmt.Errorf("offline") }
	if err := b.execute([]string{"probe", "--printer", "plus4.invalid"}); err != nil {
		t.Fatal(err)
	}
	doc, err := decodeObject(b.out.(*bytes.Buffer).Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if doc["reachable"] != false {
		t.Fatal("offline printer reported reachable")
	}
	if err := b.execute([]string{"probe", "--printer", "-bad"}); err == nil {
		t.Fatal("unsafe host accepted")
	}
	if err := b.execute([]string{"probe", "--printer", "plus4.invalid", "--probe", "unknown"}); err == nil {
		t.Fatal("unknown probe accepted")
	}
}
func TestNativeBackupValidationReadsGzipTrailer(t *testing.T) {
	var buffer bytes.Buffer
	compressed := gzip.NewWriter(&buffer)
	archive := tar.NewWriter(compressed)
	archive.WriteHeader(&tar.Header{Name: "config/printer.cfg", Size: 4, Mode: 0600})
	archive.Write([]byte("test"))
	archive.Close()
	compressed.Close()
	path := filepath.Join(t.TempDir(), "backup.tar.gz")
	os.WriteFile(path, buffer.Bytes(), 0600)
	if err := verifyBackupArchive(path); err != nil {
		t.Fatal(err)
	}
	raw := append([]byte(nil), buffer.Bytes()...)
	raw[len(raw)-8] ^= 0xff
	os.WriteFile(path, raw, 0600)
	if err := verifyBackupArchive(path); err == nil {
		t.Fatal("corrupted gzip trailer accepted")
	}
}
func TestNativeArmbianQualification(t *testing.T) {
	for _, test := range []struct {
		URL string
		OK  bool
	}{{"https://example.invalid/Armbian_qidi-x6_trixie_current_6.18.54_minimal.img.xz", true}, {"https://example.invalid/Armbian_qidi-x6_trixie_current_6.18.53_minimal.img.xz", false}, {"http://example.invalid/Armbian_qidi-x6_current_6.19.0_minimal.img.xz", false}, {"https://example.invalid/unknown.img.xz", false}} {
		_, _, err := qualifiedArmbianName(test.URL, "6.18.54")
		if (err == nil) != test.OK {
			t.Errorf("%s: %v", test.URL, err)
		}
	}
}
func factoryTestBytes() []byte {
	raw := make([]byte, 4096)
	raw[510] = 0x55
	raw[511] = 0xaa
	for i, p := range []emmcPartition{{1, 0x0e, 32768, 524288}, {2, 0x83, 557056, 59441152}} {
		entry := raw[446+i*16 : 462+i*16]
		entry[4] = p.Type
		binary.LittleEndian.PutUint32(entry[8:], p.Start)
		binary.LittleEndian.PutUint32(entry[12:], p.Sectors)
	}
	return raw
}
func TestNativeFactoryImagingStreamsAndPreservesEvidence(t *testing.T) {
	b := nativeTestBackend(t)
	out := t.TempDir()
	raw := factoryTestBytes()
	if err := b.copyFactoryImage(bytes.NewReader(raw), out, map[string]any{"read_only": true}, int64(len(raw)), 1024); err != nil {
		t.Fatal(err)
	}
	image, _ := os.ReadFile(filepath.Join(out, "factory-emmc-user-area.img"))
	if !reflect.DeepEqual(raw, image) {
		t.Fatal("image bytes differ")
	}
	if err := b.copyFactoryImage(bytes.NewReader(raw), out, nil, int64(len(raw)), 1024); err == nil {
		t.Fatal("existing evidence overwritten")
	}
	out = t.TempDir()
	raw[510] = 0
	if err := b.copyFactoryImage(bytes.NewReader(raw), out, nil, int64(len(raw)), 1024); err == nil {
		t.Fatal("invalid MBR accepted")
	}
	entries, _ := os.ReadDir(out)
	if len(entries) != 0 {
		t.Fatal("failed image left partial evidence")
	}
	out = t.TempDir()
	if err := b.copyFactoryImage(io.LimitReader(bytes.NewReader(factoryTestBytes()), 512), out, nil, 4096, 1024); err == nil {
		t.Fatal("short read accepted")
	}
}

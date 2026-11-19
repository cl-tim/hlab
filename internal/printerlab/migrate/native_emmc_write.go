package migrate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ulikunitz/xz"
)

// EMMCDisk is the identity shown before approval, rechecked before offlining,
// and checked against the opened raw handle before any image bytes are written.
type EMMCDisk struct {
	Device            string `json:"device"`
	Number            int    `json:"number"`
	FriendlyName      string `json:"friendlyName"`
	SerialNumber      string `json:"serialNumber"`
	UniqueID          string `json:"uniqueId"`
	BusType           string `json:"busType"`
	Size              int64  `json:"size"`
	LogicalSectorSize int64  `json:"logicalSectorSize"`
	IsBoot            bool   `json:"isBoot"`
	IsSystem          bool   `json:"isSystem"`
	IsReadOnly        bool   `json:"isReadOnly"`
	IsOffline         bool   `json:"isOffline"`
}

type EMMCWritePlan struct {
	Schema      string   `json:"schema"`
	Manifest    string   `json:"manifest"`
	Image       string   `json:"image"`
	ImageSHA256 string   `json:"imageSha256"`
	RawImage    string   `json:"rawImage"`
	RawSHA256   string   `json:"rawSha256"`
	Bytes       int64    `json:"bytes"`
	Disk        EMMCDisk `json:"disk"`
	CreatedAt   string   `json:"createdAt"`
}

type emmcHandle interface {
	io.ReadWriteSeeker
	Sync() error
	Close() error
}
type emmcPlatform interface {
	List() ([]EMMCDisk, error)
	Inspect(string) (EMMCDisk, error)
	Offline(EMMCDisk) error
	Open(EMMCDisk, bool) (emmcHandle, error)
}

func (b *nativeBackend) emmcPlatform() emmcPlatform {
	if b.emmc != nil {
		return b.emmc
	}
	return platformEMMC(b)
}

func writableEMMC(d EMMCDisk) error {
	if !strings.EqualFold(d.BusType, "USB") || d.IsBoot || d.IsSystem || d.IsReadOnly {
		return fmt.Errorf("target must be a writable USB disk, never a boot or system disk")
	}
	if d.Size <= 0 || d.LogicalSectorSize != 512 || strings.TrimSpace(d.SerialNumber) == "" || strings.TrimSpace(d.UniqueID) == "" {
		return fmt.Errorf("target needs 512-byte sectors, a capacity, a serial number and a unique disk ID")
	}
	return nil
}

func sameEMMCDisk(a, b EMMCDisk) bool {
	// Offline state is deliberately excluded: it changes during this operation.
	a.IsOffline, b.IsOffline = false, false
	return a == b
}

func (b *nativeBackend) prepareEMMCWrite() error {
	image, err := b.required("image")
	if err != nil {
		return err
	}
	image, err = localPath(image)
	if err != nil {
		return err
	}
	device, err := b.required("device")
	if err != nil {
		return err
	}
	disk, err := b.emmcPlatform().Inspect(device)
	if err != nil {
		return err
	}
	if err := writableEMMC(disk); err != nil {
		return err
	}
	info, err := os.Lstat(image)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("Armbian source must be a regular .img.xz file")
	}
	metadata, err := os.ReadFile(image + ".json")
	if err != nil {
		return fmt.Errorf("verified Armbian receipt missing: %w", err)
	}
	doc, err := decodeObject(metadata)
	if err != nil {
		return err
	}
	url, _ := doc["url"].(string)
	name, kernel, err := qualifiedArmbianName(url, b.value("minimum-kernel"))
	if err != nil {
		return err
	}
	if filepath.Base(image) != name || doc["filename"] != name || doc["kernel"] != kernel {
		return fmt.Errorf("image does not match its qualified Armbian receipt")
	}
	source, err := os.Open(image)
	if err != nil {
		return err
	}
	defer source.Close()
	hash := sha256.New()
	if _, err := b.copyEMMCStream(hash, source, -1); err != nil {
		return err
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if doc["sha256"] != digest {
		return fmt.Errorf("Armbian image differs from its verified checksum")
	}
	checksum, err := os.ReadFile(image + ".sha")
	if err != nil {
		return err
	}
	fields := strings.Fields(string(checksum))
	if len(fields) == 0 || strings.ToLower(fields[0]) != digest {
		return fmt.Errorf("Armbian checksum file does not match the image")
	}
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return err
	}
	reader, err := (xz.ReaderConfig{DictCap: 128 * 1024 * 1024}).NewReader(source)
	if err != nil {
		return err
	}
	root := filepath.Join(b.home, "artifacts", "armbian")
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	dir, err := os.MkdirTemp(root, ".emmc-write-")
	if err != nil {
		return err
	}
	published := false
	defer func() {
		if !published {
			os.RemoveAll(dir)
		}
	}()
	plan := EMMCWritePlan{Schema: "printerlab/emmc-write/v1", Manifest: filepath.Join(dir, "plan.json"), Image: image, ImageSHA256: digest, RawImage: filepath.Join(dir, "armbian.img"), Disk: disk, CreatedAt: utcStamp()}
	raw, err := os.OpenFile(plan.RawImage, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer raw.Close()
	fmt.Fprintln(b.errOut, "Validating and decompressing Armbian...")
	hash.Reset()
	plan.Bytes, err = b.copyEMMCStream(io.MultiWriter(raw, hash), io.LimitReader(reader, disk.Size+1), -1)
	if err != nil {
		return fmt.Errorf("decompress Armbian: %w", err)
	}
	if plan.Bytes > disk.Size || plan.Bytes < 512 || plan.Bytes%512 != 0 {
		return fmt.Errorf("image size %d is invalid or exceeds the target capacity %d", plan.Bytes, disk.Size)
	}
	sector := make([]byte, 512)
	if _, err := raw.ReadAt(sector, 0); err != nil {
		return err
	}
	if sector[510] != 0x55 || sector[511] != 0xaa {
		return fmt.Errorf("Armbian image lacks a valid MBR signature")
	}
	if err := raw.Sync(); err != nil {
		return err
	}
	plan.RawSHA256 = hex.EncodeToString(hash.Sum(nil))
	document, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return err
	}
	if err := exclusiveWrite(plan.Manifest, append(document, '\n')); err != nil {
		return err
	}
	published = true
	return b.writeJSON(plan)
}

func (b *nativeBackend) loadEMMCPlan() (EMMCWritePlan, string, error) {
	var plan EMMCWritePlan
	manifest, err := b.required("plan")
	if err != nil {
		return plan, "", err
	}
	manifest, err = filepath.Abs(manifest)
	if err != nil {
		return plan, "", err
	}
	root, err := filepath.Abs(filepath.Join(b.home, "artifacts", "armbian"))
	if err != nil {
		return plan, "", err
	}
	dir := filepath.Dir(manifest)
	if !sameLocalPath(filepath.Dir(dir), root) || !strings.HasPrefix(filepath.Base(dir), ".emmc-write-") || filepath.Base(manifest) != "plan.json" {
		return plan, "", fmt.Errorf("write plan is outside the private Armbian staging directory")
	}
	for _, path := range []string{root, dir, manifest} {
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || (path == manifest && !info.Mode().IsRegular()) || (path != manifest && !info.IsDir()) {
			return plan, "", fmt.Errorf("unsafe write plan path: %s", path)
		}
	}
	raw, err := os.ReadFile(manifest)
	if err != nil {
		return plan, "", err
	}
	if err := json.Unmarshal(raw, &plan); err != nil {
		return plan, "", err
	}
	if plan.Schema != "printerlab/emmc-write/v1" || !sameLocalPath(plan.Manifest, manifest) || !sameLocalPath(plan.RawImage, filepath.Join(dir, "armbian.img")) || !digestPattern.MatchString(plan.RawSHA256) || !digestPattern.MatchString(plan.ImageSHA256) || plan.Bytes < 512 || plan.Bytes%512 != 0 || plan.Bytes > plan.Disk.Size {
		return plan, "", fmt.Errorf("invalid Armbian write plan")
	}
	if err := writableEMMC(plan.Disk); err != nil {
		return plan, "", err
	}
	return plan, dir, nil
}

func (b *nativeBackend) writeArmbianEMMC() error {
	if err := b.confirm("WRITE-EMMC"); err != nil {
		return err
	}
	plan, dir, err := b.loadEMMCPlan()
	if err != nil {
		return err
	}
	info, err := os.Lstat(plan.RawImage)
	if err != nil || !info.Mode().IsRegular() || info.Size() != plan.Bytes {
		return fmt.Errorf("staged image is missing or changed; prepare another write plan")
	}
	image, err := os.Open(plan.RawImage)
	if err != nil {
		return err
	}
	defer image.Close()
	hash := sha256.New()
	if _, err := b.copyEMMCStream(hash, image, -1); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != plan.RawSHA256 {
		return fmt.Errorf("staged image checksum changed; target has not been modified")
	}
	if _, err := image.Seek(0, io.SeekStart); err != nil {
		return err
	}
	platform := b.emmcPlatform()
	current, err := platform.Inspect(plan.Disk.Device)
	if err != nil {
		return err
	}
	if err := writableEMMC(current); err != nil {
		return err
	}
	if !sameEMMCDisk(current, plan.Disk) {
		return fmt.Errorf("target disk identity changed after approval; select it again")
	}
	if err := b.ctx.Err(); err != nil {
		return err
	}
	// Everything above is read-only with respect to the selected disk.
	if err := platform.Offline(plan.Disk); err != nil {
		return err
	}
	disk, err := platform.Open(plan.Disk, true)
	if err != nil {
		return fmt.Errorf("open offline eMMC for writing: %w", err)
	}
	fmt.Fprintf(b.out, "Writing %d bytes to %s...\n", plan.Bytes, plan.Disk.Device)
	hash.Reset()
	_, copyErr := b.copyEMMCStream(io.MultiWriter(disk, hash), image, plan.Bytes)
	if copyErr == nil && hex.EncodeToString(hash.Sum(nil)) != plan.RawSHA256 {
		copyErr = fmt.Errorf("staged image changed during writing")
	}
	if copyErr == nil {
		copyErr = disk.Sync()
	}
	closeErr := disk.Close()
	if copyErr != nil {
		return fmt.Errorf("eMMC write incomplete; disk remains offline, rewrite before use: %w", copyErr)
	}
	if closeErr != nil {
		return closeErr
	}
	// Reopen the device so read-back uses a fresh handle after flushing writes.
	disk, err = platform.Open(plan.Disk, false)
	if err != nil {
		return fmt.Errorf("open eMMC for read-back: %w", err)
	}
	fmt.Fprintln(b.out, "Reading back the complete written image...")
	hash.Reset()
	_, readErr := b.copyEMMCStream(hash, disk, plan.Bytes)
	closeErr = disk.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	if hex.EncodeToString(hash.Sum(nil)) != plan.RawSHA256 {
		return fmt.Errorf("eMMC read-back checksum mismatch; do not install this module")
	}
	receipt := map[string]any{"schema": "printerlab/emmc-write-receipt/v1", "disk": plan.Disk, "image": plan.Image, "image_sha256": plan.ImageSHA256, "written_sha256": plan.RawSHA256, "verified_bytes": plan.Bytes, "verified_at_utc": utcStamp()}
	raw, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "receipt.json")
	if err := exclusiveWrite(path, append(raw, '\n')); err != nil {
		return fmt.Errorf("eMMC verified but receipt could not be saved: %w", err)
	}
	image.Close()
	if err := os.Remove(plan.RawImage); err != nil {
		fmt.Fprintln(b.errOut, "Verified image staging file retained:", plan.RawImage)
	}
	fmt.Fprintf(b.out, "Armbian eMMC write verified. Receipt: %s\nThe disk is offline. Disconnect the USB reader before removing the module.\n", path)
	return nil
}

// copyEMMCStream checks cancellation between bounded reads/writes and reports
// progress. exact < 0 copies to EOF; otherwise a short stream is an error.
func (b *nativeBackend) copyEMMCStream(dst io.Writer, src io.Reader, exact int64) (int64, error) {
	buffer := make([]byte, 4*1024*1024)
	var total int64
	next := int64(1024 * 1024 * 1024)
	started := time.Now()
	for exact < 0 || total < exact {
		if err := b.ctx.Err(); err != nil {
			return total, err
		}
		chunk := buffer
		if exact >= 0 && exact-total < int64(len(chunk)) {
			chunk = chunk[:exact-total]
		}
		n, readErr := src.Read(chunk)
		if n > 0 {
			written, err := dst.Write(chunk[:n])
			total += int64(written)
			if err != nil {
				return total, err
			}
			if written != n {
				return total, io.ErrShortWrite
			}
			if total >= next {
				fmt.Fprintf(b.errOut, "%d MiB processed (%s)\n", total/(1024*1024), time.Since(started).Round(time.Second))
				next += 1024 * 1024 * 1024
			}
		}
		if readErr != nil {
			if readErr == io.EOF && (exact < 0 || total == exact) {
				return total, nil
			}
			if readErr == io.EOF {
				readErr = io.ErrUnexpectedEOF
			}
			return total, readErr
		}
		if n == 0 {
			return total, io.ErrNoProgress
		}
	}
	return total, nil
}

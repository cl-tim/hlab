package migrate

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var imageKinds = map[string]string{"mainboard": MainboardImage, "toolhead-app": ToolheadImage, "toolhead-katapult": ToolheadKatapultImage}

func (b *nativeBackend) validateImage(kind, path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	layoutsRaw, err := b.helper("mcu-layouts.json")
	if err != nil {
		return err
	}
	var layouts map[string][]string
	if err := json.Unmarshal(layoutsRaw, &layouts); err != nil {
		return err
	}
	layout := layouts[kind]
	if len(layout) != 4 {
		return fmt.Errorf("unknown MCU image kind %q", kind)
	}
	limits := make([]uint64, 4)
	for i, value := range layout {
		limits[i], err = strconv.ParseUint(value, 0, 32)
		if err != nil {
			return err
		}
	}
	if len(raw) < 8 || uint64(len(raw)) > limits[1]-limits[0] {
		return fmt.Errorf("%s image size %d is outside its flash region", kind, len(raw))
	}
	stack := uint64(binary.LittleEndian.Uint32(raw[:4]))
	reset := uint64(binary.LittleEndian.Uint32(raw[4:8]))
	if stack < limits[2] || stack >= limits[3] {
		return fmt.Errorf("%s stack pointer is outside RAM", kind)
	}
	if reset&1 == 0 || reset&^1 < limits[0] || reset&^1 >= limits[1] {
		return fmt.Errorf("%s reset vector is outside its Thumb flash region", kind)
	}
	return nil
}
func verifyBuildMetadata(directory string) (map[string]string, error) {
	raw, err := os.ReadFile(filepath.Join(directory, "BUILD-METADATA.txt"))
	if err != nil {
		return nil, fmt.Errorf("build manifest missing; rebuild before migration: %w", err)
	}
	values := map[string]string{}
	hashes := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if key, value, ok := strings.Cut(line, "="); ok {
			if _, exists := values[key]; exists {
				return nil, fmt.Errorf("duplicate manifest field %s", key)
			}
			values[key] = value
		} else if digest, name, ok := strings.Cut(line, "  "); ok {
			if _, exists := hashes[name]; exists {
				return nil, fmt.Errorf("duplicate image hash %s", name)
			}
			hashes[name] = digest
		}
	}
	for _, name := range imageKinds {
		actual, err := hashFile(filepath.Join(directory, name))
		if err != nil || hashes[name] != actual || !digestPattern.MatchString(hashes[name]) {
			return nil, fmt.Errorf("build manifest hash mismatch or missing image: %s", name)
		}
	}
	for _, key := range []string{"klipper_commit", "katapult_commit"} {
		if !commitPattern.MatchString(values[key]) {
			return nil, fmt.Errorf("build manifest lacks pinned %s", key)
		}
	}
	return values, nil
}
func (b *nativeBackend) verifiedImage(kind string) (string, error) {
	image, err := b.required("image")
	if err != nil {
		return "", err
	}
	image, err = localPath(image)
	if err != nil {
		return "", err
	}
	if dir := b.value("artifacts"); dir != "" {
		dir, err = localPath(dir)
		if err != nil {
			return "", err
		}
		if _, err := verifyBuildMetadata(dir); err != nil {
			return "", err
		}
		if !sameLocalPath(image, filepath.Join(dir, imageKinds[kind])) {
			return "", fmt.Errorf("selected image is outside the verified artifact set")
		}
	}
	if err := b.validateImage(kind, image); err != nil {
		return "", err
	}
	return image, nil
}
func wslPath(value string) string {
	value = strings.ReplaceAll(value, "\\", "/")
	if len(value) >= 3 && value[1] == ':' && value[2] == '/' {
		return "/mnt/" + strings.ToLower(value[:1]) + value[2:]
	}
	return value
}
func (b *nativeBackend) buildFirmware() error {
	klipper, err := b.required("klipper-dir")
	if err != nil {
		return err
	}
	katapult, err := b.required("katapult-dir")
	if err != nil {
		return err
	}
	for _, key := range []string{"klipper-ref", "katapult-ref"} {
		if !commitPattern.MatchString(b.value(key)) {
			return fmt.Errorf("--%s must be a full pinned commit", key)
		}
	}
	output := b.value("output")
	if output == "" {
		output = filepath.Join(b.home, "artifacts", "mainline-firmware")
	}
	output, err = localPath(output)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(output, 0700); err != nil {
		return err
	}
	root := b.root
	program := "bash"
	prefix := []string{}
	toolchain := "unknown"
	if distro := b.value("wsl-distro"); distro != "" {
		if strings.HasPrefix(distro, "-") || strings.ContainsAny(distro, "\x00\r\n") {
			return fmt.Errorf("invalid WSL distribution")
		}
		program = "wsl"
		prefix = []string{"-d", distro, "--", "bash"}
		if raw, _, err := b.capture("wsl", []string{"-d", distro, "--", "arm-none-eabi-gcc", "--version"}, nil); err == nil {
			toolchain = strings.SplitN(strings.TrimSpace(string(raw)), "\n", 2)[0]
		}
		root = wslPath(root)
		outputWSL := wslPath(output)
		script, err := b.helper("build-firmware.sh")
		if err != nil {
			return err
		}
		args := append(prefix, "-s", "--", root, wslPath(klipper), wslPath(katapult), outputWSL, b.value("klipper-ref"), b.value("katapult-ref"))
		if err := b.run(program, args, script); err != nil {
			return err
		}
	} else {
		if runtime.GOOS == "windows" {
			return fmt.Errorf("firmware compilation on Windows requires --wsl-distro")
		}
		if raw, _, err := b.capture("arm-none-eabi-gcc", []string{"--version"}, nil); err == nil {
			toolchain = strings.SplitN(strings.TrimSpace(string(raw)), "\n", 2)[0]
		}
		klipper, err = localPath(klipper)
		if err != nil {
			return err
		}
		katapult, err = localPath(katapult)
		if err != nil {
			return err
		}
		script, err := b.helper("build-firmware.sh")
		if err != nil {
			return err
		}
		if err := b.run(program, []string{"-s", "--", root, klipper, katapult, output, b.value("klipper-ref"), b.value("katapult-ref")}, script); err != nil {
			return err
		}
	}
	lines := []string{"built_at_utc=" + utcStamp(), "klipper_commit=" + b.value("klipper-ref"), "katapult_commit=" + b.value("katapult-ref"), "arm_none_eabi_gcc=" + strings.TrimSpace(toolchain)}
	for _, kind := range []string{"mainboard", "toolhead-app", "toolhead-katapult"} {
		name := imageKinds[kind]
		path := filepath.Join(output, name)
		if err := b.validateImage(kind, path); err != nil {
			return err
		}
		digest, err := hashFile(path)
		if err != nil {
			return err
		}
		lines = append(lines, digest+"  "+name)
	}
	raw := []byte(strings.Join(lines, "\n") + "\n")
	if err := atomicWrite(filepath.Join(output, "BUILD-METADATA.txt"), raw); err != nil {
		return err
	}
	_, err = b.out.Write(raw)
	return err
}
func (b *nativeBackend) stageMainboard() error {
	image, err := b.verifiedImage("mainboard")
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
	if err := os.MkdirAll(output, 0700); err != nil {
		return err
	}
	target := filepath.Join(output, "qd_mcu.bin")
	if info, err := os.Lstat(target); err == nil {
		if !b.switches["force"] || !info.Mode().IsRegular() {
			return fmt.Errorf("%s already exists (use --force only for a regular file)", target)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	raw, err := os.ReadFile(image)
	if err != nil {
		return err
	}
	if err := atomicWrite(target, raw); err != nil {
		return err
	}
	digest, err := hashFile(target)
	if err != nil {
		return err
	}
	if digest != hashBytes(raw) {
		return fmt.Errorf("staged image readback mismatch")
	}
	if err := atomicWrite(target+".sha256", []byte(digest+"  qd_mcu.bin\n")); err != nil {
		return err
	}
	fmt.Fprintf(b.out, "Staged mainboard firmware: %s\nSHA-256 %s\n", target, digest)
	return nil
}

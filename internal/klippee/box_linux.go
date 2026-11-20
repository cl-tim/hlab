package klippee

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

var boxDeviceName = regexp.MustCompile(`^usb-Klipper_QIDI_BOX_V1_1\.1\.1_([0-9A-F]{24})(?:-if00)?$`)
var boxTTY = regexp.MustCompile(`^/dev/ttyACM[0-9]{1,4}$`)

func (c *Client) configuredBox(ctx context.Context) (string, error) {
	raw, err := c.moonrakerCall(ctx, "printer.objects.query", map[string]any{
		"objects": map[string]any{"webhooks": []string{"state"}, "configfile": []string{"settings"}},
	})
	if err != nil {
		return "", err
	}
	var data struct {
		Status struct {
			Webhooks   struct{ State string }
			ConfigFile struct {
				Settings map[string]struct {
					Serial        string
					RestartMethod string `json:"restart_method"`
				}
			}
		}
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return "", err
	}
	if data.Status.Webhooks.State != "shutdown" {
		return "", fmt.Errorf("Box reset-reason query requires a fresh Klipper shutdown state")
	}
	mcu := data.Status.ConfigFile.Settings["mcu mcu_box1"]
	if mcu.RestartMethod != "command" || filepath.Dir(mcu.Serial) != "/dev/serial/by-id" {
		return "", fmt.Errorf("Moonraker did not identify the supported QIDI Box endpoint")
	}
	return mcu.Serial, nil
}

func resolveBox(path string) (map[string]string, uint64, error) {
	match := boxDeviceName.FindStringSubmatch(filepath.Base(path))
	if match == nil || filepath.Dir(path) != "/dev/serial/by-id" {
		return nil, 0, fmt.Errorf("unsupported QIDI Box device name")
	}
	if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSymlink == 0 {
		return nil, 0, fmt.Errorf("QIDI Box endpoint must be a USB by-id symlink")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || !boxTTY.MatchString(resolved) {
		return nil, 0, fmt.Errorf("unsupported QIDI Box TTY")
	}
	var stat unix.Stat_t
	if err := unix.Stat(resolved, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFCHR {
		return nil, 0, fmt.Errorf("QIDI Box endpoint is not a character device")
	}
	device, err := filepath.EvalSymlinks(filepath.Join("/sys/class/tty", filepath.Base(resolved), "device"))
	if err != nil {
		return nil, 0, err
	}
	for !usbPath.MatchString(filepath.Base(device)) {
		parent := filepath.Dir(device)
		if parent == device {
			return nil, 0, fmt.Errorf("could not identify the QIDI Box USB device")
		}
		device = parent
	}
	endpoint := map[string]string{"configured": path, "resolved": resolved,
		"vendor_id":    systemText(filepath.Join(device, "idVendor"), 16),
		"product_id":   systemText(filepath.Join(device, "idProduct"), 16),
		"manufacturer": systemText(filepath.Join(device, "manufacturer"), 256),
		"product":      systemText(filepath.Join(device, "product"), 256),
		"serial":       systemText(filepath.Join(device, "serial"), 256)}
	if endpoint["vendor_id"] != "1d50" || endpoint["product_id"] != "614e" ||
		endpoint["manufacturer"] != "Klipper" || endpoint["product"] != "QIDI_BOX_V1_1.1.1" || endpoint["serial"] != match[1] {
		return nil, 0, fmt.Errorf("QIDI Box USB identity mismatch")
	}
	if current, err := filepath.EvalSymlinks(path); err != nil || current != resolved {
		return nil, 0, fmt.Errorf("QIDI Box endpoint changed during identity validation")
	}
	return endpoint, uint64(stat.Rdev), nil
}

func requireBoxTTYUnowned(device uint64, ignorePID int) error {
	processes, err := os.ReadDir("/proc")
	if err != nil {
		return fmt.Errorf("verify QIDI Box TTY ownership: %w", err)
	}
	for _, process := range processes {
		pid, err := strconv.Atoi(process.Name())
		if err != nil || pid == ignorePID {
			continue
		}
		dir := filepath.Join("/proc", process.Name(), "fd")
		fds, err := os.ReadDir(dir)
		if err != nil {
			if errors.Is(err, os.ErrPermission) || errors.Is(err, os.ErrNotExist) {
				continue
			}
			return fmt.Errorf("inspect QIDI Box TTY ownership: %w", err)
		}
		for _, fd := range fds {
			path := filepath.Join(dir, fd.Name())
			link, err := os.Readlink(path)
			if err != nil || strings.HasSuffix(link, " (deleted)") {
				continue
			}
			var stat unix.Stat_t
			if unix.Stat(path, &stat) == nil && stat.Mode&unix.S_IFMT == unix.S_IFCHR && uint64(stat.Rdev) == device {
				return fmt.Errorf("QIDI Box TTY is already open by process %d", pid)
			}
		}
	}
	return nil
}

func boxSerialSetup(fd int) error {
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return err
	}
	if err := unix.IoctlSetInt(fd, unix.TIOCEXCL, 0); err != nil {
		return err
	}
	config, err := unix.IoctlGetTermios(fd, unix.TCGETS2)
	if err != nil {
		return err
	}
	config.Iflag, config.Oflag, config.Lflag = 0, 0, 0
	config.Cflag = unix.BOTHER | unix.CS8 | unix.CREAD | unix.CLOCAL
	config.Ispeed, config.Ospeed = 250000, 250000
	config.Cc[unix.VMIN], config.Cc[unix.VTIME] = 1, 0
	if err := unix.IoctlSetTermios(fd, unix.TCSETS2, config); err != nil {
		return err
	}
	config, err = unix.IoctlGetTermios(fd, unix.TCGETS2)
	if err != nil || config.Ispeed != 250000 || config.Ospeed != 250000 {
		return fmt.Errorf("QIDI Box TTY did not accept 250000 baud")
	}
	return nil
}

type linuxBoxWire struct {
	fd      int
	pending []byte
}

func waitBoxIO(ctx context.Context, fd int, event int16) error {
	for ctx.Err() == nil {
		poll := []unix.PollFd{{Fd: int32(fd), Events: event}}
		if _, err := unix.Poll(poll, 40); err != nil && err != unix.EINTR {
			return err
		}
		if poll[0].Revents&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 {
			return fmt.Errorf("QIDI Box TTY disconnected")
		}
		if poll[0].Revents&event != 0 {
			return nil
		}
	}
	return ctx.Err()
}

func (w *linuxBoxWire) WritePacket(ctx context.Context, data []byte) error {
	for len(data) > 0 {
		if err := waitBoxIO(ctx, w.fd, unix.POLLOUT); err != nil {
			return err
		}
		n, err := unix.Write(w.fd, data)
		if err == unix.EAGAIN || err == unix.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func (w *linuxBoxWire) ReadPacket(ctx context.Context) (byte, []byte, error) {
	var input [128]byte
	for {
		for len(w.pending) > 0 {
			length := int(w.pending[0])
			if length < 5 || length > 64 {
				w.pending = w.pending[1:]
				continue
			}
			if len(w.pending) < length {
				break
			}
			frame := w.pending[:length]
			crc := boxCRC(frame[:length-3])
			if frame[1]&0xf0 == 0x10 && frame[length-1] == 0x7e && frame[length-3] == byte(crc>>8) && frame[length-2] == byte(crc) {
				w.pending = w.pending[length:]
				return frame[1] & 0xf, frame[2 : length-3], nil
			}
			w.pending = w.pending[1:]
		}
		if err := waitBoxIO(ctx, w.fd, unix.POLLIN); err != nil {
			return 0, nil, err
		}
		n, err := unix.Read(w.fd, input[:])
		if err == unix.EINTR || err == unix.EAGAIN {
			continue
		}
		if err != nil {
			return 0, nil, err
		}
		if n == 0 {
			return 0, nil, io.EOF
		}
		w.pending = append(w.pending, input[:n]...)
	}
}

func (c *Client) queryBoxReset(ctx context.Context) (map[string]any, error) {
	if c.Config.ClientMode != "printer" {
		return nil, fmt.Errorf("Box reset-reason query requires a printer connection")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	path, err := c.configuredBox(ctx)
	if err != nil {
		return nil, err
	}
	endpoint, device, err := resolveBox(path)
	if err != nil {
		return nil, err
	}
	lock, err := c.lock()
	if err != nil {
		return nil, err
	}
	defer lock()
	if err := requireBoxTTYUnowned(device, 0); err != nil {
		return nil, err
	}
	fd, err := unix.Open(endpoint["resolved"], unix.O_RDWR|unix.O_NOCTTY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(fd)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFCHR || uint64(stat.Rdev) != device {
		return nil, fmt.Errorf("QIDI Box TTY changed while opening")
	}
	if err := boxSerialSetup(fd); err != nil {
		return nil, fmt.Errorf("configure exclusive QIDI Box serial session: %w", err)
	}
	revalidate := func() error {
		current, err := c.configuredBox(ctx)
		if err != nil || current != path {
			return fmt.Errorf("could not revalidate the shutdown state and QIDI Box configuration")
		}
		resolved, currentDevice, err := resolveBox(path)
		if err != nil || currentDevice != device || !maps.Equal(endpoint, resolved) {
			return fmt.Errorf("QIDI Box endpoint changed during the query")
		}
		return requireBoxTTYUnowned(device, os.Getpid())
	}
	if err := revalidate(); err != nil {
		return nil, err
	}
	session := boxSession{wire: &linuxBoxWire{fd: fd}}
	flags, reason, err := session.readReset(ctx, validateBoxDictionary, revalidate)
	if err != nil {
		return nil, err
	}
	if err := revalidate(); err != nil {
		return nil, err
	}
	return boxResetResult(endpoint, flags, reason), nil
}

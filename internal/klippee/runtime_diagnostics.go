package klippee

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (c *Client) collectRuntimeDiagnostics(ctx context.Context, b *diagnosticBundle) error {
	snapshots := []struct {
		name, method string
		params       any
	}{
		{"server-info", "server.info", nil},
		{"printer-info", "printer.info", nil},
		{"temperature-store", "server.temperature_store", nil},
		{"gcode-store", "server.gcode_store", map[string]int{"count": 1000}},
		{"job-history", "server.history.list", map[string]any{"limit": 20, "order": "desc"}},
		{"system-info", "machine.system_info", nil},
		{"process-stats", "machine.proc_stats", nil},
		{"printer-objects-list", "printer.objects.list", nil},
	}
	var printer, server, objects json.RawMessage
	for _, snapshot := range snapshots {
		if snapshot.name != "server-info" && len(server) == 0 {
			break
		}
		raw, err := c.moonrakerCall(ctx, snapshot.method, snapshot.params)
		if err != nil {
			b.warnings = append(b.warnings, snapshot.name+" was unavailable.")
			continue
		}
		switch snapshot.name {
		case "server-info":
			server = raw
		case "printer-info":
			printer = raw
		case "printer-objects-list":
			objects = raw
		}
		if err := b.addJSON("snapshots/"+snapshot.name+".json", "moonraker-snapshot", map[string]json.RawMessage{"result": raw}); err != nil {
			return err
		}
	}
	var available struct{ Objects []string }
	_ = json.Unmarshal(objects, &available)
	query := map[string]any{}
	hasVariables := false
	for _, name := range available.Objects {
		if name == "save_variables" {
			hasVariables = true
			continue
		}
		if diagnosticObject(name) {
			query[name] = nil
		}
	}
	if len(query) > 0 {
		raw, err := c.moonrakerCall(ctx, "printer.objects.query", map[string]any{"objects": query})
		if err != nil {
			b.warnings = append(b.warnings, "printer-objects was unavailable.")
		} else if err := b.addJSON("snapshots/printer-objects.json", "moonraker-snapshot", map[string]json.RawMessage{"result": raw}); err != nil {
			return err
		}
	}
	if hasVariables {
		raw, err := c.moonrakerCall(ctx, "printer.objects.query", map[string]any{"objects": map[string]any{"save_variables": nil}})
		if err != nil {
			b.warnings = append(b.warnings, "save-variables was unavailable.")
		} else if safe, err := safeDiagnosticVariables(raw); err != nil {
			b.warnings = append(b.warnings, "save-variables snapshot was invalid.")
		} else if err := b.addJSON("snapshots/save-variables.json", "moonraker-snapshot", safe); err != nil {
			return err
		}
	}
	host := c.hostInfo(printer, server)
	host["generatedAt"] = time.Now().UTC().Format(time.RFC3339)
	if err := b.addJSON("snapshots/host-info.json", "system-snapshot", host); err != nil {
		return err
	}
	if err := b.addJSON("snapshots/usb-serial-topology.json", "system-snapshot", usbSerialTopology()); err != nil {
		return err
	}
	if err := b.addJSON("snapshots/host-scheduling.json", "system-snapshot", hostScheduling(ctx)); err != nil {
		return err
	}
	return b.addJournals(ctx)
}

func diagnosticObject(name string) bool {
	switch name {
	case "configfile", "display_status", "heaters", "print_stats", "system_stats", "toolhead", "virtual_sdcard", "webhooks":
		return true
	}
	for _, prefix := range []string{"bed_mesh", "fan", "filament_", "heater", "mcu", "probe", "temperature_", "tmc"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func (b *diagnosticBundle) addJournals(ctx context.Context) error {
	journal, err := exec.LookPath("journalctl")
	if err != nil {
		b.warnings = append(b.warnings, "journalctl was unavailable.")
		return nil
	}
	commands := []struct {
		name string
		args []string
	}{
		{"services", []string{"--lines=2000", "-u", "klipper", "-u", "moonraker"}},
		{"user-services", []string{"--user", "--lines=2000", "-u", "klipper", "-u", "moonraker"}},
		{"kernel", []string{"--dmesg", "--lines=1500"}},
	}
	for _, item := range commands {
		timeout, cancel := context.WithTimeout(ctx, 8*time.Second)
		cmd := exec.CommandContext(timeout, journal, append([]string{"--no-pager", "--output=short-iso"}, item.args...)...)
		var tail journalTail
		cmd.Stdout = &tail
		err := cmd.Run()
		cancel()
		if err != nil || len(bytes.TrimSpace(tail.data)) == 0 {
			b.warnings = append(b.warnings, item.name+" journal was unavailable.")
			continue
		}
		if err := b.add("logs/"+item.name+"-journal.log", "journal", bytes.NewReader(tail.data), tail.total, int64(len(tail.data))); err != nil {
			return err
		}
	}
	return ctx.Err()
}

type journalTail struct {
	data  []byte
	total int64
}

func (b *journalTail) Write(data []byte) (int, error) {
	const limit = 2 << 20
	b.total += int64(len(data))
	if len(data) >= limit {
		b.data = append(b.data[:0], data[len(data)-limit:]...)
		return len(data), nil
	}
	if remove := len(b.data) + len(data) - limit; remove > 0 {
		b.data = b.data[:copy(b.data, b.data[remove:])]
	}
	b.data = append(b.data, data...)
	return len(data), nil
}

func systemText(path string, limit int64) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

var ttyName = regexp.MustCompile(`^tty(?:ACM|USB)[0-9]{1,4}$`)
var usbPath = regexp.MustCompile(`^[0-9]+-[0-9]+(?:\.[0-9]+)*$`)

func usbSerialTopology() map[string]any {
	devices := []map[string]string{}
	entries, _ := os.ReadDir("/dev/serial/by-id")
	for _, entry := range entries[:min(64, len(entries))] {
		if entry.Type()&os.ModeSymlink == 0 || len(entry.Name()) > 240 {
			continue
		}
		path := "/dev/serial/by-id/" + entry.Name()
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil || filepath.Dir(resolved) != "/dev" || !ttyName.MatchString(filepath.Base(resolved)) {
			continue
		}
		device, err := filepath.EvalSymlinks(filepath.Join("/sys/class/tty", filepath.Base(resolved), "device"))
		if err != nil {
			continue
		}
		for dir := device; filepath.Dir(dir) != dir; dir = filepath.Dir(dir) {
			if usbPath.MatchString(filepath.Base(dir)) {
				devices = append(devices, map[string]string{
					"byId": path, "tty": resolved, "usbPath": filepath.Base(dir),
					"manufacturer": systemText(filepath.Join(dir, "manufacturer"), 256),
					"product":      systemText(filepath.Join(dir, "product"), 256),
					"productId":    systemText(filepath.Join(dir, "idProduct"), 16),
					"serial":       systemText(filepath.Join(dir, "serial"), 256),
					"vendorId":     systemText(filepath.Join(dir, "idVendor"), 16),
				})
				break
			}
		}
	}
	return map[string]any{"devices": devices}
}

func hostScheduling(ctx context.Context) map[string]any {
	governors := map[string]string{}
	policies, _ := filepath.Glob("/sys/devices/system/cpu/cpufreq/policy*")
	sort.Strings(policies)
	for _, policy := range policies[:min(64, len(policies))] {
		if governor := systemText(filepath.Join(policy, "scaling_governor"), 80); governor != "" {
			governors[filepath.Base(policy)] = governor
		}
	}
	irqs := []map[string]any{}
	ttys, _ := os.ReadDir("/sys/class/tty")
	for _, tty := range ttys[:min(256, len(ttys))] {
		irq, err := strconv.Atoi(systemText(filepath.Join("/sys/class/tty", tty.Name(), "device", "irq"), 32))
		if err != nil || irq < 0 {
			continue
		}
		dir := fmt.Sprintf("/proc/irq/%d", irq)
		irqs = append(irqs, map[string]any{"device": tty.Name(), "irq": irq,
			"requestedCpuList": systemText(filepath.Join(dir, "smp_affinity_list"), 256),
			"effectiveCpuList": systemText(filepath.Join(dir, "effective_affinity_list"), 256)})
		if len(irqs) == 64 {
			break
		}
	}
	services := []map[string]string{}
	for _, name := range []string{"klipper", "moonraker", "nginx", "crowsnest"} {
		values := serviceProperties(ctx, name+".service", "MainPID", "ActiveState", "CPUAffinity")
		item := map[string]string{"name": name, "activeState": values["ActiveState"], "cpuAffinity": values["CPUAffinity"], "cpuAllowedList": ""}
		if pid, err := strconv.Atoi(values["MainPID"]); err == nil && pid > 0 {
			status := systemText(fmt.Sprintf("/proc/%d/status", pid), 128<<10)
			for _, line := range strings.Split(status, "\n") {
				if affinity, ok := strings.CutPrefix(line, "Cpus_allowed_list:"); ok {
					item["cpuAllowedList"] = strings.TrimSpace(affinity)
					break
				}
			}
		}
		services = append(services, item)
	}
	return map[string]any{"onlineCpus": systemText("/sys/devices/system/cpu/online", 256), "governors": governors,
		"serialIrqs": irqs, "services": services,
		"irqbalanceState": serviceProperties(ctx, "irqbalance.service", "ActiveState")["ActiveState"],
		"ondemandState":   serviceProperties(ctx, "ondemand.service", "ActiveState")["ActiveState"]}
}

func serviceProperties(ctx context.Context, unit string, properties ...string) map[string]string {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	args := []string{"show", unit, "--no-pager"}
	for _, property := range properties {
		args = append(args, "--property="+property)
	}
	values, err := exec.CommandContext(ctx, "systemctl", args...).Output()
	if err != nil {
		return nil
	}
	return keyValues(values)
}

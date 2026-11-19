package migrate

import (
	"fmt"
	"os"
	"strings"
)

type nativeProbeDefinition struct {
	ID          string `json:"id"`
	Stage       string `json:"stage"`
	Description string `json:"description"`
}

var nativeProbeDefinitions = []nativeProbeDefinition{
	{"host.reachable", "transport", "Facts were collected from the printer"},
	{"host.stock-client", "baseline", "The stock QIDI client service is still installed"},
	{"bootstrap.receipt", "bootstrap-host", "bootstrap-mainline.sh recorded a completed install"},
	{"bootstrap.sources", "bootstrap-host", "Upstream Klipper, Moonraker and Katapult checkouts exist"},
	{"bootstrap.services", "bootstrap-host", "Klipper and Moonraker are installed as system services"},
	{"bootstrap.uart-claimed", "bootstrap-host", "Both MCU UARTs are masked from getty and owned by dialout"},
	{"host-tuning.installed", "tune-host", "Persistent RK3328 host latency tuning is installed"},
	{"config.smoke-test", "smoke-test", "The inert two-MCU smoke-test printer.cfg is installed"},
	{"config.installed", "render-config", "A real two-MCU printer.cfg is installed"},
	{"mcu.online", "flash-toolhead", "Both printer MCUs are connected to mainline Klipper"},
}

type nativeProbeResult struct {
	Probe    string         `json:"probe"`
	Stage    string         `json:"stage"`
	Status   string         `json:"status"`
	Detail   string         `json:"detail"`
	Evidence map[string]any `json:"evidence"`
}

func lookupFact(facts map[string]any, path string) (any, bool) {
	var node any = facts
	for _, part := range strings.Split(path, ".") {
		object, ok := node.(map[string]any)
		if !ok {
			return nil, false
		}
		node, ok = object[part]
		if !ok {
			return nil, false
		}
	}
	return node, true
}
func factText(value any) string {
	if value == nil {
		return "None"
	}
	return fmt.Sprint(value)
}
func oneOf(value any, options ...string) bool {
	text, ok := value.(string)
	if !ok {
		return false
	}
	for _, option := range options {
		if text == option {
			return true
		}
	}
	return false
}
func factList(value any) ([]any, bool) { list, ok := value.([]any); return list, ok }
func listHas(list []any, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}
func evaluateNativeProbe(def nativeProbeDefinition, facts map[string]any, smokeSHA string) nativeProbeResult {
	result := nativeProbeResult{Probe: def.ID, Stage: def.Stage, Status: "indeterminate", Evidence: map[string]any{}}
	get := func(path string) any { value, _ := lookupFact(facts, path); return value }
	require := func(paths ...string) bool {
		missing := []string{}
		for _, path := range paths {
			value, present := lookupFact(facts, path)
			result.Evidence[path] = value
			if !present {
				missing = append(missing, path)
			}
		}
		if len(missing) > 0 {
			result.Status = "indeterminate"
			result.Detail = "printer did not report: " + strings.Join(missing, ", ")
			return false
		}
		return true
	}
	evidence := func(paths ...string) {
		for _, path := range paths {
			result.Evidence[path] = get(path)
		}
	}
	satisfied := func(detail string) { result.Status = "satisfied"; result.Detail = detail }
	unsatisfied := func(detail string) { result.Status = "unsatisfied"; result.Detail = detail }
	switch def.ID {
	case "host.reachable":
		evidence("hostname", "collected_at_utc")
		stamp, present := lookupFact(facts, "collected_at_utc")
		if !present {
			result.Detail = "no facts were collected from the printer"
		} else {
			satisfied("facts collected at " + factText(stamp))
		}
	case "host.stock-client":
		if !require("services.makerbase-client", "services_enabled.makerbase-client") {
			break
		}
		active := get("services.makerbase-client")
		enabled := get("services_enabled.makerbase-client")
		if active == "active" || oneOf(enabled, "enabled", "enabled-runtime", "static") {
			satisfied("makerbase-client is installed")
		} else if active == "not-found" && enabled == "not-found" {
			unsatisfied("makerbase-client is absent")
		} else {
			unsatisfied("makerbase-client is present but inactive (" + factText(active) + ")")
		}
	case "bootstrap.receipt":
		if !require("bootstrap_receipt.present") {
			break
		}
		evidence("bootstrap_receipt.installed_at_utc", "bootstrap_receipt.klipper_commit", "bootstrap_receipt.moonraker_commit", "bootstrap_receipt.katapult_commit")
		if get("bootstrap_receipt.present") == true {
			satisfied("completed bootstrap receipt is present")
		} else {
			unsatisfied("no completed bootstrap receipt")
		}
	case "bootstrap.sources":
		if !require("checkouts.klipper.present", "checkouts.moonraker.present", "checkouts.katapult.present") {
			break
		}
		missing := []string{}
		for _, name := range []string{"klipper", "moonraker", "katapult"} {
			evidence("checkouts." + name + ".head")
			if get("checkouts."+name+".present") != true {
				missing = append(missing, name)
			}
		}
		if len(missing) == 0 {
			satisfied("all upstream checkouts are present")
		} else {
			unsatisfied("upstream checkouts absent: " + strings.Join(missing, ", "))
		}
	case "bootstrap.services":
		if !require("services_enabled.klipper", "services_enabled.moonraker") {
			break
		}
		evidence("services.klipper", "services.moonraker")
		missing := []string{}
		for _, name := range []string{"klipper", "moonraker"} {
			if !oneOf(get("services_enabled."+name), "enabled", "enabled-runtime", "static", "indirect") {
				missing = append(missing, name)
			}
		}
		if len(missing) == 0 {
			satisfied("Klipper and Moonraker are installed as system services")
		} else {
			unsatisfied("services not installed: " + strings.Join(missing, ", "))
		}
	case "bootstrap.uart-claimed":
		if !require("serial_getty.ttyS0", "serial_getty.ttyS2", "udev_rule") {
			break
		}
		reasons := []string{}
		for _, tty := range []string{"ttyS0", "ttyS2"} {
			if !oneOf(get("serial_getty."+tty), "masked", "masked-runtime") {
				reasons = append(reasons, "serial console still enabled on "+tty)
			}
		}
		if get("udev_rule") != true {
			reasons = append(reasons, "99-qidi-uarts.rules is absent")
		}
		if len(reasons) == 0 {
			satisfied("both UARTs are masked and owned by dialout")
		} else {
			unsatisfied(strings.Join(reasons, "; "))
		}
	case "host-tuning.installed":
		if !require("services_enabled.qidi-plus4-host-tuning") {
			break
		}
		evidence("services.qidi-plus4-host-tuning", "bootstrap_receipt.qidi_host_tuning_version")
		if oneOf(get("services_enabled.qidi-plus4-host-tuning"), "enabled", "enabled-runtime", "static") {
			satisfied("persistent host tuning is installed")
		} else {
			unsatisfied("persistent host tuning is not installed")
		}
	case "config.smoke-test":
		result.Evidence["expected_sha256"] = smokeSHA
		if smokeSHA == "" {
			result.Detail = "local smoke-test configuration digest is unavailable"
			break
		}
		if !require("printer_cfg.present") {
			break
		}
		if get("printer_cfg.present") != true {
			unsatisfied("printer.cfg is absent")
			break
		}
		if !require("printer_cfg.sha256") {
			break
		}
		if get("printer_cfg.sha256") == smokeSHA {
			satisfied("inert two-MCU smoke-test config is installed")
		} else {
			unsatisfied("printer.cfg differs from the inert smoke-test config")
		}
	case "mcu.online":
		if !require("moonraker.reachable") {
			break
		}
		evidence("moonraker.klippy_state", "moonraker.klippy_message", "moonraker.mcu_objects")
		if get("moonraker.reachable") != true {
			result.Detail = "Moonraker did not answer"
			break
		}
		state, present := lookupFact(facts, "moonraker.klippy_state")
		if !present {
			result.Detail = "Moonraker answered without a Klippy state"
			break
		}
		if state == "startup" {
			result.Detail = "Klippy is starting up"
			break
		}
		if state != "ready" {
			message, _ := get("moonraker.klippy_message").(string)
			detail := "klippy state is " + factText(state)
			if message != "" {
				detail += ": " + strings.SplitN(message, "\n", 2)[0]
			}
			unsatisfied(detail)
			break
		}
		objects, ok := factList(get("moonraker.mcu_objects"))
		if !ok {
			result.Detail = "MCU objects were not collected"
			break
		}
		if len(objects) < 2 {
			unsatisfied(fmt.Sprintf("only %d configured MCU objects", len(objects)))
		} else {
			satisfied(fmt.Sprintf("%d MCUs are connected", len(objects)))
		}
	case "config.installed":
		if !require("printer_cfg.present") {
			break
		}
		if get("printer_cfg.present") != true {
			unsatisfied("printer.cfg is absent")
			break
		}
		evidence("printer_cfg.size", "printer_cfg.sha256", "printer_cfg.mcu_sections", "bootstrap_receipt.present")
		size := get("printer_cfg.size")
		if size == float64(0) || size == 0 || size == false {
			unsatisfied("printer.cfg is an empty placeholder")
			break
		}
		if smokeSHA != "" && get("printer_cfg.sha256") == smokeSHA {
			unsatisfied("the inert smoke-test configuration is still installed")
			break
		}
		if sections, ok := factList(get("printer_cfg.mcu_sections")); ok && len(sections) < 2 {
			unsatisfied("printer.cfg has fewer than two MCU sections")
			break
		}
		if !require("printer_cfg.size", "printer_cfg.sha256", "printer_cfg.mcu_sections", "bootstrap_receipt.present") {
			break
		}
		if get("bootstrap_receipt.present") != true {
			unsatisfied("no completed mainline bootstrap receipt; stock configs do not qualify")
			break
		}
		online := evaluateNativeProbe(nativeProbeDefinitions[9], facts, smokeSHA)
		for key, value := range online.Evidence {
			result.Evidence[key] = value
		}
		if online.Status != "satisfied" {
			result.Status = online.Status
			result.Detail = "installed config has not passed the live MCU check: " + online.Detail
			break
		}
		objects, _ := factList(get("moonraker.mcu_objects"))
		if !listHas(objects, "mcu") || !(listHas(objects, "mcu toolhead") || listHas(objects, "mcu U_1")) {
			unsatisfied("live MCU names do not match the two-MCU printer configuration")
		} else {
			satisfied("a real two-MCU printer configuration is installed and connected")
		}
	}
	return result
}
func selectNativeProbes(ids []string) ([]nativeProbeDefinition, error) {
	if len(ids) == 0 {
		return nativeProbeDefinitions, nil
	}
	selected := []nativeProbeDefinition{}
	for _, id := range ids {
		found := false
		for _, def := range nativeProbeDefinitions {
			if def.ID == id {
				selected = append(selected, def)
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("unknown probe %q", id)
		}
	}
	return selected, nil
}
func (b *nativeBackend) probePrinter() error {
	selected, err := selectNativeProbes(b.probes)
	if err != nil {
		return err
	}
	var facts map[string]any
	reason := ""
	if filename := b.value("facts-file"); filename != "" {
		raw, err := os.ReadFile(filename)
		if err != nil {
			return err
		}
		facts, err = decodeObject(raw)
		if err != nil {
			return err
		}
	} else {
		facts, reason, err = b.collectFacts()
		if err != nil {
			return err
		}
	}
	smoke, _ := b.helper("smoke-test-printer.cfg")
	smokeSHA := ""
	if len(smoke) > 0 {
		smokeSHA = hashBytes(smoke)
	}
	results := []nativeProbeResult{}
	summary := map[string]int{"satisfied": 0, "unsatisfied": 0, "indeterminate": 0}
	for _, def := range selected {
		var result nativeProbeResult
		if facts == nil {
			result = nativeProbeResult{def.ID, def.Stage, "indeterminate", reason, map[string]any{}}
		} else {
			result = evaluateNativeProbe(def, facts, smokeSHA)
		}
		results = append(results, result)
		summary[result.Status]++
	}
	report := map[string]any{"schema": 1, "printer": b.value("printer"), "reachable": facts != nil, "probes": results, "summary": summary}
	if b.switches["include-facts"] {
		report["facts"] = facts
	}
	return b.writeJSON(report)
}

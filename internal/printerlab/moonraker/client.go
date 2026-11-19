// Package moonraker provides the common management plane shared by stock and
// mainline QIDI firmware.
package moonraker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/cl-tim/homelab-cli/internal/printerlab/inventory"
)

type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// ExtensionResponse is the acknowledgement returned by Klippee's Moonraker
// agent. The acknowledgement means the command was accepted for asynchronous
// execution; it does not mean that the command has finished successfully.
type ExtensionResponse struct {
	Accepted    bool   `json:"accepted"`
	Command     string `json:"command"`
	OperationID string `json:"operation_id,omitempty"`
}

func (response *ExtensionResponse) UnmarshalJSON(data []byte) error {
	fields, err := decodeExactJSONObject(
		data,
		"Klippee extension acknowledgement",
		[]string{"accepted", "command", "operation_id"},
		[]string{"accepted", "command"},
	)
	if err != nil {
		return err
	}
	var decoded struct {
		Accepted    *bool  `json:"accepted"`
		Command     string `json:"command"`
		OperationID string `json:"operation_id"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	if decoded.Accepted == nil {
		return fmt.Errorf("Klippee extension acknowledgement omitted required accepted proof")
	}
	if raw, present := fields["operation_id"]; present && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		decoded.OperationID = ""
	}
	*response = ExtensionResponse{
		Accepted: *decoded.Accepted, Command: decoded.Command,
		OperationID: decoded.OperationID,
	}
	return nil
}

// ExtensionOperation describes one bounded asynchronous operation retained by
// Klippee's Moonraker agent. ReturnCode is nil while the command is running or
// when a process could not return a code (for example, a timeout).
type ExtensionOperation struct {
	ID          string `json:"id"`
	Method      string `json:"method"`
	Command     string `json:"command"`
	Status      string `json:"status"`
	StartedAt   string `json:"started_at"`
	CompletedAt string `json:"completed_at,omitempty"`
	ReturnCode  *int   `json:"return_code,omitempty"`
	Summary     string `json:"summary,omitempty"`
	// Result carries a method-specific, structured operation result. Callers
	// must decode and validate it against the schema for the exact method they
	// requested; arbitrary command output remains confined to Summary.
	Result json.RawMessage `json:"result,omitempty"`
}

func (operation *ExtensionOperation) UnmarshalJSON(data []byte) error {
	fields, err := decodeExactJSONObject(
		data,
		"Klippee operation",
		[]string{
			"id", "method", "command", "status", "started_at", "completed_at",
			"return_code", "summary", "result",
		},
		[]string{"id", "method", "command", "status", "started_at"},
	)
	if err != nil {
		return err
	}
	type operationJSON ExtensionOperation
	var decoded operationJSON
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	if decoded.Status != "running" {
		if _, present := fields["completed_at"]; !present {
			return fmt.Errorf("completed Klippee operation omitted completed_at")
		}
		if _, present := fields["return_code"]; !present {
			return fmt.Errorf("completed Klippee operation omitted return_code")
		}
	}
	*operation = ExtensionOperation(decoded)
	return nil
}

// ExtensionOperationStatus contains the agent's current or most recent operation.
// Older operation IDs may have expired.
type ExtensionOperationStatus struct {
	Running     bool                `json:"running"`
	Found       bool                `json:"found"`
	OperationID string              `json:"operation_id,omitempty"`
	Current     *ExtensionOperation `json:"current,omitempty"`
	LastResult  *ExtensionOperation `json:"last_result,omitempty"`
}

func (status *ExtensionOperationStatus) UnmarshalJSON(data []byte) error {
	if _, err := decodeExactJSONObject(
		data,
		"Klippee operation status",
		[]string{"running", "found", "operation_id", "current", "last_result"},
		[]string{"running", "found", "operation_id", "current", "last_result"},
	); err != nil {
		return err
	}
	var decoded struct {
		Running     *bool               `json:"running"`
		Found       *bool               `json:"found"`
		OperationID string              `json:"operation_id"`
		Current     *ExtensionOperation `json:"current"`
		LastResult  *ExtensionOperation `json:"last_result"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	if decoded.Running == nil || decoded.Found == nil {
		return fmt.Errorf("Klippee operation status omitted required running/found proof")
	}
	*status = ExtensionOperationStatus{
		Running: *decoded.Running, Found: *decoded.Found,
		OperationID: decoded.OperationID, Current: decoded.Current,
		LastResult: decoded.LastResult,
	}
	return nil
}

func decodeExactJSONObject(
	data []byte,
	label string,
	allowed []string,
	required []string,
) (map[string]json.RawMessage, error) {
	if err := rejectDuplicateJSONNames(data); err != nil {
		return nil, fmt.Errorf("%s is ambiguous: %w", label, err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil || object == nil {
		if err != nil {
			return nil, fmt.Errorf("%s must be a JSON object: %w", label, err)
		}
		return nil, fmt.Errorf("%s must be a JSON object", label)
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, name := range allowed {
		allowedSet[name] = struct{}{}
	}
	for name := range object {
		if _, ok := allowedSet[name]; !ok {
			return nil, fmt.Errorf("%s contains unexpected exact-case field %q", label, name)
		}
	}
	for _, name := range required {
		if _, ok := object[name]; !ok {
			return nil, fmt.Errorf("%s omitted required field %q", label, name)
		}
	}
	return object, nil
}

func rejectDuplicateJSONNames(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := consumeUniqueJSONValue(decoder); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err != nil {
			return fmt.Errorf("decode trailing JSON: %w", err)
		}
		return fmt.Errorf("response contains more than one JSON value")
	}
	return nil
}

func consumeUniqueJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			nameToken, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := nameToken.(string)
			if !ok {
				return fmt.Errorf("JSON object contains a non-string field name")
			}
			if _, duplicate := seen[name]; duplicate {
				return fmt.Errorf("response contains duplicate field %q", name)
			}
			seen[name] = struct{}{}
			if err := consumeUniqueJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return fmt.Errorf("response contains an unterminated object")
		}
	case '[':
		for decoder.More() {
			if err := consumeUniqueJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return fmt.Errorf("response contains an unterminated array")
		}
	default:
		return fmt.Errorf("response contains unexpected delimiter %q", delimiter)
	}
	return nil
}

type extensionRequest struct {
	Agent     string `json:"agent"`
	Method    string `json:"method"`
	Arguments any    `json:"arguments,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	if e.Code != 0 {
		return fmt.Sprintf("Moonraker extension request rejected (%d): %s", e.Code, e.Message)
	}
	return fmt.Sprintf("Moonraker extension request rejected: %s", e.Message)
}

func New(printer inventory.Printer, timeout time.Duration) (*Client, error) {
	baseURL := strings.TrimRight(printer.MoonrakerURL, "/")
	if baseURL == "" {
		baseURL = fmt.Sprintf("http://%s:%d", printer.Address, printer.MoonrakerPort)
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid Moonraker URL %q", baseURL)
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	apiKey := ""
	if printer.APIKeyEnv != "" {
		apiKey = os.Getenv(printer.APIKeyEnv)
		if apiKey == "" {
			return nil, fmt.Errorf("Moonraker API key environment variable %s is empty", printer.APIKeyEnv)
		}
	}
	return &Client{
		baseURL: baseURL,
		apiKey:  apiKey,
		http: &http.Client{
			Timeout: timeout,
			// Never forward Moonraker requests across an HTTP redirect. In
			// particular, X-Api-Key is a custom credential header that Go may
			// otherwise copy to the redirect target.
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

type Snapshot struct {
	Name               string    `json:"name" yaml:"name"`
	Address            string    `json:"address" yaml:"address"`
	Model              string    `json:"model,omitempty" yaml:"model,omitempty"`
	Firmware           string    `json:"firmware" yaml:"firmware"`
	Hostname           string    `json:"hostname,omitempty" yaml:"hostname,omitempty"`
	MoonrakerVersion   string    `json:"moonrakerVersion,omitempty" yaml:"moonrakerVersion,omitempty"`
	KlipperVersion     string    `json:"klipperVersion,omitempty" yaml:"klipperVersion,omitempty"`
	KlippyState        string    `json:"klippyState" yaml:"klippyState"`
	KlippyStateMessage string    `json:"klippyStateMessage,omitempty" yaml:"klippyStateMessage,omitempty"`
	PrintState         string    `json:"printState,omitempty" yaml:"printState,omitempty"`
	Filename           string    `json:"filename,omitempty" yaml:"filename,omitempty"`
	PrintDuration      float64   `json:"printDurationSeconds,omitempty" yaml:"printDurationSeconds,omitempty"`
	Progress           float64   `json:"progress,omitempty" yaml:"progress,omitempty"`
	ExtruderTemp       float64   `json:"extruderTemp,omitempty" yaml:"extruderTemp,omitempty"`
	ExtruderTarget     float64   `json:"extruderTarget,omitempty" yaml:"extruderTarget,omitempty"`
	BedTemp            float64   `json:"bedTemp,omitempty" yaml:"bedTemp,omitempty"`
	BedTarget          float64   `json:"bedTarget,omitempty" yaml:"bedTarget,omitempty"`
	HomedAxes          string    `json:"homedAxes,omitempty" yaml:"homedAxes,omitempty"`
	ToolheadPosition   []float64 `json:"toolheadPosition,omitempty" yaml:"toolheadPosition,omitempty"`
	IdleState          string    `json:"idleState,omitempty" yaml:"idleState,omitempty"`
	Paused             bool      `json:"paused" yaml:"paused"`
	Reachable          bool      `json:"reachable" yaml:"reachable"`
	Error              string    `json:"error,omitempty" yaml:"error,omitempty"`
	ConfigurationBasis string    `json:"configurationBasis" yaml:"configurationBasis"`
}

type BoxOutputClockStatus struct {
	State       string `json:"state" yaml:"state"`
	PreviousMCU string `json:"previousMcu" yaml:"previousMcu"`
	PinMCU      string `json:"pinMcu" yaml:"pinMcu"`
}

type BoxGuardStatus struct {
	State                     string                          `json:"state" yaml:"state"`
	CorrectedCount            int                             `json:"correctedCount" yaml:"correctedCount"`
	CorrectedOutputs          []string                        `json:"correctedOutputs" yaml:"correctedOutputs"`
	Outputs                   map[string]BoxOutputClockStatus `json:"outputs" yaml:"outputs"`
	ReloadAllGuarded          bool                            `json:"reloadAllGuarded" yaml:"reloadAllGuarded"`
	ConfigSectionLoaded       bool                            `json:"configSectionLoaded" yaml:"configSectionLoaded"`
	MotionSensorSectionLoaded bool                            `json:"motionSensorSectionLoaded" yaml:"motionSensorSectionLoaded"`
	UseIRQ                    bool                            `json:"useIrq" yaml:"useIrq"`
	UseIRQKnown               bool                            `json:"useIrqKnown" yaml:"useIrqKnown"`
	DebounceUS                int                             `json:"debounceUs,omitempty" yaml:"debounceUs,omitempty"`
	DebounceUSPresent         bool                            `json:"debounceUsPresent" yaml:"debounceUsPresent"`
	DebounceUSKnown           bool                            `json:"debounceUsKnown" yaml:"debounceUsKnown"`
	AutoInsertEnabled         bool                            `json:"autoInsertEnabled" yaml:"autoInsertEnabled"`
	AutoInsertVariablePresent bool                            `json:"autoInsertVariablePresent" yaml:"autoInsertVariablePresent"`
	BoxCount                  int                             `json:"boxCount" yaml:"boxCount"`
}

type BoxRuntimeStatus struct {
	LoadMacroLoaded   bool           `json:"loadMacroLoaded" yaml:"loadMacroLoaded"`
	UnloadMacroLoaded bool           `json:"unloadMacroLoaded" yaml:"unloadMacroLoaded"`
	UnloadReady       bool           `json:"unloadReady" yaml:"unloadReady"`
	UnloadSlot        string         `json:"unloadSlot,omitempty" yaml:"unloadSlot,omitempty"`
	UnloadSlotSource  string         `json:"unloadSlotSource,omitempty" yaml:"unloadSlotSource,omitempty"`
	UnloadIssue       string         `json:"unloadIssue,omitempty" yaml:"unloadIssue,omitempty"`
	Enabled           bool           `json:"enabled" yaml:"enabled"`
	BoxCount          int            `json:"boxCount" yaml:"boxCount"`
	ExtrudeState      int            `json:"extrudeState" yaml:"extrudeState"`
	SlotSync          string         `json:"slotSync" yaml:"slotSync"`
	LastLoadSlot      string         `json:"lastLoadSlot" yaml:"lastLoadSlot"`
	RetryStep         string         `json:"retryStep,omitempty" yaml:"retryStep,omitempty"`
	BEndstop          bool           `json:"bEndstop" yaml:"bEndstop"`
	EEndstop          bool           `json:"eEndstop" yaml:"eEndstop"`
	FilamentWidth     float64        `json:"filamentWidth" yaml:"filamentWidth"`
	SlotMappings      map[int]string `json:"slotMappings" yaml:"slotMappings"`
	SlotRunout        map[int]bool   `json:"slotRunout" yaml:"slotRunout"`
	SlotState         map[int]int    `json:"slotState" yaml:"slotState"`
	PrintState        string         `json:"printState" yaml:"printState"`
	HomedAxes         string         `json:"homedAxes" yaml:"homedAxes"`
}

type boxRuntimeEnvelope struct {
	Result struct {
		Status map[string]map[string]any `json:"status"`
	} `json:"result"`
}

func runtimeString(values map[string]any, key, fallback string) string {
	value, present := values[key]
	if !present {
		return fallback
	}
	text, ok := value.(string)
	if !ok {
		return fallback
	}
	return strings.TrimSpace(text)
}

func validBoxSlot(value string) bool {
	switch value {
	case "slot0", "slot1", "slot2", "slot3":
		return true
	default:
		return false
	}
}

// BoxRuntimeStatus reads Box runtime state used to verify guard activation.
func (c *Client) BoxRuntimeStatus(ctx context.Context) (BoxRuntimeStatus, error) {
	var envelope boxRuntimeEnvelope
	path := "/printer/objects/query?save_variables&box_extras&hall_filament_width_sensor&print_stats&toolhead" +
		"&box_stepper%20slot0&box_stepper%20slot1&box_stepper%20slot2&box_stepper%20slot3" +
		"&gcode_macro%20QIDI_BOX_LOAD&gcode_macro%20QIDI_BOX_UNLOAD_LOADED"
	if err := c.get(ctx, path, &envelope); err != nil {
		return BoxRuntimeStatus{}, err
	}
	status := envelope.Result.Status
	variables := status["save_variables"]
	if nested, ok := variables["variables"].(map[string]any); ok {
		variables = nested
	}
	result := BoxRuntimeStatus{
		LoadMacroLoaded:   false,
		UnloadMacroLoaded: false,
		SlotSync:          runtimeString(variables, "slot_sync", "slot-1"),
		LastLoadSlot:      runtimeString(variables, "last_load_slot", "slot-1"),
		RetryStep:         runtimeString(variables, "retry_step", ""),
		SlotMappings:      make(map[int]string, 4),
		SlotRunout:        make(map[int]bool, 4),
		SlotState:         make(map[int]int, 4),
	}
	_, result.LoadMacroLoaded = status["gcode_macro QIDI_BOX_LOAD"]
	_, result.UnloadMacroLoaded = status["gcode_macro QIDI_BOX_UNLOAD_LOADED"]
	if value, known := flexibleBool(variables["enable_box"]); known {
		result.Enabled = value
	}
	if value, known := flexibleInt(variables["box_count"]); known {
		result.BoxCount = value
	}
	if value, known := flexibleInt(variables["extrude_state"]); known {
		result.ExtrudeState = value
	} else {
		result.ExtrudeState = -1
	}
	for index := 0; index < 4; index++ {
		result.SlotMappings[index] = runtimeString(variables, fmt.Sprintf("value_t%d", index), fmt.Sprintf("slot%d", index))
		if value, known := flexibleInt(variables[fmt.Sprintf("slot%d", index)]); known {
			result.SlotState[index] = value
		}
		stepper := status[fmt.Sprintf("box_stepper slot%d", index)]
		if value, known := flexibleBool(stepper["runout_button"]); known {
			result.SlotRunout[index] = value
		}
	}
	box := status["box_extras"]
	if value, known := flexibleBool(box["b_endstop_state"]); known {
		result.BEndstop = value
	}
	if value, known := flexibleBool(box["e_endstop_state"]); known {
		result.EEndstop = value
	}
	hall := status["hall_filament_width_sensor"]
	for _, key := range []string{"Diameter", "diameter"} {
		if value, ok := hall[key].(float64); ok {
			result.FilamentWidth = value
			break
		}
	}
	result.PrintState = runtimeString(status["print_stats"], "state", "unknown")
	result.HomedAxes = runtimeString(status["toolhead"], "homed_axes", "")
	switch {
	case validBoxSlot(result.SlotSync) && validBoxSlot(result.LastLoadSlot) && result.SlotSync != result.LastLoadSlot:
		result.UnloadIssue = "live and persisted loaded-slot state disagree"
	case validBoxSlot(result.SlotSync):
		result.UnloadSlot = result.SlotSync
		result.UnloadSlotSource = "slot_sync"
	case validBoxSlot(result.LastLoadSlot):
		result.UnloadSlot = result.LastLoadSlot
		result.UnloadSlotSource = "last_load_slot"
	default:
		result.UnloadIssue = "no loaded slot is recorded"
	}
	if result.UnloadIssue == "" && !result.UnloadMacroLoaded {
		result.UnloadIssue = "guarded unload macro is not loaded"
	}
	if result.UnloadIssue == "" && !result.EEndstop {
		result.UnloadIssue = "extruder filament sensor is not active"
	}
	if result.UnloadIssue == "" && (result.PrintState == "printing" || result.PrintState == "paused") {
		result.UnloadIssue = "printer is not idle"
	}
	result.UnloadReady = result.UnloadIssue == ""
	return result, nil
}

type boxOutputClockStatusEnvelope struct {
	State       string `json:"state"`
	PreviousMCU string `json:"previous_mcu"`
	PinMCU      string `json:"pin_mcu"`
}

type boxGuardEnvelope struct {
	Result struct {
		Status struct {
			Guard struct {
				State            string                                  `json:"state"`
				CorrectedCount   int                                     `json:"corrected_count"`
				CorrectedOutputs []string                                `json:"corrected_outputs"`
				Outputs          map[string]boxOutputClockStatusEnvelope `json:"outputs"`
				ReloadAllGuarded bool                                    `json:"reload_all_guarded"`
			} `json:"box_output_clock_guard"`
			ConfigFile struct {
				Settings map[string]map[string]any `json:"settings"`
			} `json:"configfile"`
			SaveVariables struct {
				Variables map[string]any `json:"variables"`
			} `json:"save_variables"`
		} `json:"status"`
	} `json:"result"`
}

func flexibleBool(value any) (bool, bool) {
	switch typed := value.(type) {
	case bool:
		return typed, true
	case float64:
		if typed == 0 {
			return false, true
		}
		if typed == 1 {
			return true, true
		}
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "false", "0", "off", "no":
			return false, true
		case "true", "1", "on", "yes":
			return true, true
		}
	}
	return false, false
}

func flexibleInt(value any) (int, bool) {
	switch typed := value.(type) {
	case float64:
		integer := int(typed)
		return integer, float64(integer) == typed
	case string:
		integer, err := strconv.Atoi(strings.TrimSpace(typed))
		return integer, err == nil
	}
	return 0, false
}

// BoxGuardStatus reads the Moonraker objects used to check Box guard settings.
func (c *Client) BoxGuardStatus(ctx context.Context) (BoxGuardStatus, error) {
	var envelope boxGuardEnvelope
	if err := c.get(
		ctx,
		"/printer/objects/query?box_output_clock_guard&configfile&save_variables",
		&envelope,
	); err != nil {
		return BoxGuardStatus{}, err
	}
	status := envelope.Result.Status
	result := BoxGuardStatus{
		State:            strings.TrimSpace(status.Guard.State),
		CorrectedCount:   status.Guard.CorrectedCount,
		CorrectedOutputs: status.Guard.CorrectedOutputs,
		Outputs:          make(map[string]BoxOutputClockStatus, len(status.Guard.Outputs)),
		ReloadAllGuarded: status.Guard.ReloadAllGuarded,
	}
	for name, raw := range status.Guard.Outputs {
		result.Outputs[name] = BoxOutputClockStatus{
			State: strings.TrimSpace(raw.State), PreviousMCU: raw.PreviousMCU, PinMCU: raw.PinMCU,
		}
	}
	// Klipper may omit an empty config section from configfile.settings. The
	// printer object can only exist when the section loaded successfully.
	result.ConfigSectionLoaded = result.State != ""
	motion, motionPresent := status.ConfigFile.Settings["filament_motion_sensor box_motion_sensor"]
	result.MotionSensorSectionLoaded = motionPresent
	if motionPresent {
		if value, present := motion["use_irq"]; present {
			result.UseIRQ, result.UseIRQKnown = flexibleBool(value)
		}
		if value, present := motion["debounce_us"]; present {
			result.DebounceUSPresent = true
			result.DebounceUS, result.DebounceUSKnown = flexibleInt(value)
		}
	}
	if value, present := status.SaveVariables.Variables["qidi_box_auto_insert"]; present {
		result.AutoInsertVariablePresent = true
		var known bool
		result.AutoInsertEnabled, known = flexibleBool(value)
		if !known {
			return BoxGuardStatus{}, fmt.Errorf("save_variables qidi_box_auto_insert is not boolean")
		}
	}
	for index := 1; index <= 4; index++ {
		if _, present := status.ConfigFile.Settings[fmt.Sprintf("mcu mcu_box%d", index)]; present {
			result.BoxCount++
		}
	}
	return result, nil
}

// ErrMalformedState distinguishes a successful but incomplete/invalid
// Moonraker response from an object-query failure caused by Klippy being
// disconnected. Safety fallbacks must never treat malformed data as proof.
var ErrMalformedState = errors.New("malformed Moonraker state")

// HTTPError retains the status so migration cannot confuse an authorization
// failure with a host whose Klippy service has not been installed or started.
type HTTPError struct {
	StatusCode int
	Detail     string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("Moonraker %d %s: %s", e.StatusCode, http.StatusText(e.StatusCode), e.Detail)
}

// IdleUnavailable permits a physical idle acknowledgement only for a network
// outage or an explicit server-level disconnected/startup response. Printing,
// paused, malformed JSON/state and authorization failures never qualify.
func (c *Client) IdleUnavailable(ctx context.Context, cause error) bool {
	if ctx.Err() != nil {
		return false
	}
	var network *url.Error
	if errors.As(cause, &network) {
		return true
	}
	if errors.Is(cause, ErrMalformedState) {
		// The reviewed inert smoke config intentionally has no print_stats.
		// Recognize its complete resolved settings, never an arbitrary empty
		// or malformed object-query response from a normal printer.
		var envelope struct {
			Result struct {
				Status struct {
					Config struct {
						Settings map[string]map[string]any `json:"settings"`
					} `json:"configfile"`
				} `json:"status"`
			} `json:"result"`
		}
		if c.get(ctx, "/printer/objects/query?configfile", &envelope) != nil {
			return false
		}
		settings := envelope.Result.Status.Config.Settings
		if len(settings) != 3 {
			return false
		}
		return settings["printer"]["kinematics"] == "none" &&
			settings["mcu"]["serial"] == "/dev/ttyS0" &&
			settings["mcu toolhead"]["serial"] == "/dev/ttyS2"
	}
	var response *HTTPError
	if !errors.As(cause, &response) || response.StatusCode != http.StatusServiceUnavailable {
		return false
	}
	connection, err := c.KlippyConnection(ctx)
	if err != nil {
		return false
	}
	switch connection.State {
	case "disconnected":
		return !connection.Connected
	case "startup", "error", "shutdown":
		return true
	default:
		return false
	}
}

type KlippyConnectionState struct {
	Connected bool   `json:"connected" yaml:"connected"`
	State     string `json:"state" yaml:"state"`
}

type serverInfoEnvelope struct {
	Result struct {
		KlippyConnected  bool     `json:"klippy_connected"`
		KlippyState      string   `json:"klippy_state"`
		Components       []string `json:"components"`
		FailedComponents []string `json:"failed_components"`
		MoonrakerVersion string   `json:"moonraker_version"`
		Hostname         string   `json:"hostname"`
	} `json:"result"`
}

type printerInfoEnvelope struct {
	Result struct {
		State           string `json:"state"`
		StateMessage    string `json:"state_message"`
		Hostname        string `json:"hostname"`
		SoftwareVersion string `json:"software_version"`
	} `json:"result"`
}

func boundedStateMessage(value string) string {
	normalized := strings.Join(strings.Fields(value), " ")
	runes := []rune(normalized)
	if len(runes) > 1000 {
		return string(runes[:1000]) + "..."
	}
	return normalized
}

type objectsEnvelope struct {
	Result struct {
		Status struct {
			PrintStats struct {
				State         string  `json:"state"`
				Filename      string  `json:"filename"`
				PrintDuration float64 `json:"print_duration"`
			} `json:"print_stats"`
			VirtualSDCard struct {
				Progress float64 `json:"progress"`
			} `json:"virtual_sdcard"`
			Extruder struct {
				Temperature float64 `json:"temperature"`
				Target      float64 `json:"target"`
			} `json:"extruder"`
			HeaterBed struct {
				Temperature float64 `json:"temperature"`
				Target      float64 `json:"target"`
			} `json:"heater_bed"`
			Toolhead struct {
				HomedAxes string    `json:"homed_axes"`
				Position  []float64 `json:"position"`
			} `json:"toolhead"`
			IdleTimeout struct {
				State string `json:"state"`
			} `json:"idle_timeout"`
			PauseResume struct {
				IsPaused bool `json:"is_paused"`
			} `json:"pause_resume"`
		} `json:"status"`
	} `json:"result"`
}

type printStateEnvelope struct {
	Result struct {
		Status struct {
			PrintStats struct {
				State string `json:"state"`
			} `json:"print_stats"`
		} `json:"status"`
	} `json:"result"`
}

type idleTimeoutEnvelope struct {
	Result struct {
		Status struct {
			ConfigFile struct {
				Settings struct {
					IdleTimeout struct {
						Timeout float64 `json:"timeout"`
					} `json:"idle_timeout"`
				} `json:"settings"`
			} `json:"configfile"`
		} `json:"status"`
	} `json:"result"`
}

type GcodeStoreEvent struct {
	Time    float64 `json:"time" yaml:"time"`
	Type    string  `json:"type" yaml:"type"`
	Message string  `json:"message" yaml:"message"`
}

type gcodeStoreEnvelope struct {
	Result struct {
		Events []GcodeStoreEvent `json:"gcode_store"`
	} `json:"result"`
}

type klippyConnectionEnvelope struct {
	Result struct {
		Connected *bool  `json:"klippy_connected"`
		State     string `json:"klippy_state"`
	} `json:"result"`
}

func (c *Client) Snapshot(ctx context.Context, printer inventory.Printer) Snapshot {
	snapshot := Snapshot{
		Name:               printer.Name,
		Address:            printer.Address,
		Model:              printer.Model,
		Firmware:           printer.Firmware,
		ConfigurationBasis: "inventory",
	}

	var server serverInfoEnvelope
	if err := c.get(ctx, "/server/info", &server); err != nil {
		snapshot.Error = err.Error()
		return snapshot
	}
	snapshot.Reachable = true
	snapshot.Hostname = server.Result.Hostname
	snapshot.MoonrakerVersion = server.Result.MoonrakerVersion
	snapshot.KlippyState = server.Result.KlippyState

	var printerInfo printerInfoEnvelope
	if err := c.get(ctx, "/printer/info", &printerInfo); err == nil {
		snapshot.KlipperVersion = printerInfo.Result.SoftwareVersion
		snapshot.KlippyStateMessage = boundedStateMessage(printerInfo.Result.StateMessage)
		if snapshot.Hostname == "" {
			snapshot.Hostname = printerInfo.Result.Hostname
		}
		if snapshot.KlippyState == "" {
			snapshot.KlippyState = printerInfo.Result.State
		}
	}

	var objects objectsEnvelope
	path := "/printer/objects/query?print_stats&virtual_sdcard&extruder&heater_bed&toolhead&idle_timeout&pause_resume"
	if err := c.get(ctx, path, &objects); err != nil {
		if server.Result.KlippyConnected {
			snapshot.Error = err.Error()
		}
		return snapshot
	}
	status := objects.Result.Status
	snapshot.PrintState = status.PrintStats.State
	snapshot.Filename = status.PrintStats.Filename
	snapshot.PrintDuration = status.PrintStats.PrintDuration
	snapshot.Progress = status.VirtualSDCard.Progress
	snapshot.ExtruderTemp = status.Extruder.Temperature
	snapshot.ExtruderTarget = status.Extruder.Target
	snapshot.BedTemp = status.HeaterBed.Temperature
	snapshot.BedTarget = status.HeaterBed.Target
	snapshot.HomedAxes = status.Toolhead.HomedAxes
	snapshot.ToolheadPosition = status.Toolhead.Position
	snapshot.IdleState = status.IdleTimeout.State
	snapshot.Paused = status.PauseResume.IsPaused
	return snapshot
}

func (c *Client) EnsureIdle(ctx context.Context) error {
	state, err := c.PrintState(ctx)
	if err != nil {
		return fmt.Errorf("cannot verify printer state: %w", err)
	}
	switch state {
	case "printing", "paused":
		return fmt.Errorf("printer state is %q; destructive operations require an idle printer", state)
	case "standby", "complete", "cancelled", "error":
		return nil
	case "":
		return fmt.Errorf("Moonraker returned no print_stats.state")
	default:
		return fmt.Errorf("unrecognised printer state %q; refusing destructive operation", state)
	}
}

// PrintState returns one authoritative print_stats.state. A missing state is
// explicitly marked malformed so callers cannot reinterpret it as a normal
// Klippy-disconnected object-query failure.
func (c *Client) PrintState(ctx context.Context) (string, error) {
	var envelope printStateEnvelope
	if err := c.get(ctx, "/printer/objects/query?print_stats", &envelope); err != nil {
		return "", err
	}
	state := strings.TrimSpace(envelope.Result.Status.PrintStats.State)
	if state == "" {
		return "", fmt.Errorf("%w: Moonraker returned no print_stats.state", ErrMalformedState)
	}
	return state, nil
}

// IdleTimeout returns only the resolved idle_timeout.timeout setting rather
// than exposing the printer's complete resolved configuration.
func (c *Client) IdleTimeout(ctx context.Context) (float64, error) {
	var envelope idleTimeoutEnvelope
	if err := c.get(ctx, "/printer/objects/query?configfile", &envelope); err != nil {
		return 0, err
	}
	seconds := envelope.Result.Status.ConfigFile.Settings.IdleTimeout.Timeout
	if seconds <= 0 {
		return 0, fmt.Errorf("%w: Moonraker returned no positive idle timeout", ErrMalformedState)
	}
	return seconds, nil
}

// KlippeeEvents returns only recent bounded messages that identify Klippee;
// unrelated console history is never returned to the caller.
func (c *Client) KlippeeEvents(ctx context.Context) ([]GcodeStoreEvent, error) {
	var envelope gcodeStoreEnvelope
	if err := c.get(ctx, "/server/gcode_store?count=1000", &envelope); err != nil {
		return nil, err
	}
	events := make([]GcodeStoreEvent, 0, 32)
	for _, event := range envelope.Result.Events {
		message := boundedStateMessage(event.Message)
		if !strings.Contains(strings.ToLower(message), "klippee") {
			continue
		}
		event.Message = message
		events = append(events, event)
		if len(events) > 50 {
			events = events[len(events)-50:]
		}
	}
	return events, nil
}

// KlippyConnection reads Moonraker's server-level connection evidence. Pointer
// decoding makes an omitted false-valued flag distinguishable from an explicit
// authoritative false.
func (c *Client) KlippyConnection(ctx context.Context) (KlippyConnectionState, error) {
	var envelope klippyConnectionEnvelope
	if err := c.get(ctx, "/server/info", &envelope); err != nil {
		return KlippyConnectionState{}, err
	}
	if envelope.Result.Connected == nil {
		return KlippyConnectionState{}, fmt.Errorf(
			"%w: Moonraker returned no klippy_connected flag", ErrMalformedState,
		)
	}
	state := strings.ToLower(strings.TrimSpace(envelope.Result.State))
	if state == "" {
		return KlippyConnectionState{}, fmt.Errorf(
			"%w: Moonraker returned no klippy_state", ErrMalformedState,
		)
	}
	return KlippyConnectionState{Connected: *envelope.Result.Connected, State: state}, nil
}

// WaitForKlippyReady observes Moonraker after a separately confirmed systemd
// start. It never retries the start itself. Transient connection/startup states
// are bounded by timeout; terminal, contradictory, malformed, and unknown
// states fail immediately.
func (c *Client) WaitForKlippyReady(
	ctx context.Context,
	timeout time.Duration,
	interval time.Duration,
) (KlippyConnectionState, error) {
	if timeout <= 0 {
		return KlippyConnectionState{}, fmt.Errorf("Klippy readiness timeout must be positive")
	}
	if interval <= 0 {
		return KlippyConnectionState{}, fmt.Errorf("Klippy readiness poll interval must be positive")
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var last KlippyConnectionState
	var lastErr error
	for {
		state, err := c.KlippyConnection(waitCtx)
		if err == nil {
			last = state
			switch state.State {
			case "ready":
				if !state.Connected {
					return state, fmt.Errorf("contradictory Moonraker state: ready but not connected")
				}
				return state, nil
			case "startup":
				if !state.Connected {
					// A newly spawned Klippy may not have completed the socket
					// handshake yet; remain within the same bounded observation.
				}
			case "disconnected":
				if state.Connected {
					return state, fmt.Errorf("contradictory Moonraker state: connected but disconnected")
				}
			case "error", "shutdown":
				return state, fmt.Errorf("Klippy entered terminal state %q", state.State)
			default:
				return state, fmt.Errorf("unknown Moonraker klippy_state %q", state.State)
			}
			lastErr = fmt.Errorf("Klippy is %s (connected=%t)", state.State, state.Connected)
		} else {
			if errors.Is(err, ErrMalformedState) {
				return KlippyConnectionState{}, err
			}
			lastErr = err
		}

		timer := time.NewTimer(interval)
		select {
		case <-waitCtx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			if lastErr == nil {
				lastErr = waitCtx.Err()
			}
			return last, fmt.Errorf("Klippy did not become ready within %s: %w", timeout, lastErr)
		case <-timer.C:
		}
	}
}

// RequestExtension calls a Moonraker agent method through HTTP.
// It accepts wrapped and direct results and rejects JSON-RPC errors.
func (c *Client) RequestExtension(
	ctx context.Context,
	agent string,
	method string,
	arguments any,
	target any,
) error {
	agent = strings.TrimSpace(agent)
	method = strings.TrimSpace(method)
	if agent == "" {
		return fmt.Errorf("Moonraker extension agent is empty")
	}
	if method == "" {
		return fmt.Errorf("Moonraker extension method is empty")
	}
	if target == nil {
		return fmt.Errorf("Moonraker extension response target is nil")
	}

	payload, err := json.Marshal(extensionRequest{Agent: agent, Method: method, Arguments: arguments})
	if err != nil {
		return fmt.Errorf("encode Moonraker extension request: %w", err)
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		c.baseURL+"/server/extensions/request",
		bytes.NewReader(payload),
	)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		request.Header.Set("X-Api-Key", c.apiKey)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read Moonraker extension response: %w", err)
	}

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var envelope struct {
			Error *rpcError `json:"error"`
		}
		decodedEnvelope := json.Unmarshal(body, &envelope) == nil
		if decodedEnvelope && envelope.Error != nil {
			return fmt.Errorf("Moonraker %s: %w", response.Status, envelope.Error)
		}
		return &HTTPError{StatusCode: response.StatusCode, Detail: strings.TrimSpace(string(body))}
	}
	if !json.Valid(body) {
		return fmt.Errorf("decode Moonraker extension response: invalid JSON")
	}
	if err := rejectDuplicateJSONNames(body); err != nil {
		return fmt.Errorf("decode Moonraker extension response: %w", err)
	}

	result := json.RawMessage(body)
	var topLevel map[string]json.RawMessage
	if json.Unmarshal(body, &topLevel) == nil && topLevel != nil {
		_, hasResult := topLevel["result"]
		_, hasError := topLevel["error"]
		if hasResult || hasError {
			if hasResult && hasError {
				return fmt.Errorf("Moonraker extension response contains both result and error")
			}
			allowedEnvelopeFields := map[string]struct{}{
				"jsonrpc": {}, "id": {}, "result": {}, "error": {},
			}
			for name := range topLevel {
				if _, allowed := allowedEnvelopeFields[name]; !allowed {
					return fmt.Errorf(
						"Moonraker extension envelope contains unexpected exact-case field %q",
						name,
					)
				}
			}
			if rawVersion, present := topLevel["jsonrpc"]; present {
				var version string
				if json.Unmarshal(rawVersion, &version) != nil || version != "2.0" {
					return fmt.Errorf("Moonraker extension envelope has an invalid jsonrpc version")
				}
			}
			if hasError {
				var rpcErr *rpcError
				if err := json.Unmarshal(topLevel["error"], &rpcErr); err != nil || rpcErr == nil {
					return fmt.Errorf("Moonraker extension envelope contains an invalid error")
				}
				return rpcErr
			}
			result = topLevel["result"]
		}
	}
	if len(bytes.TrimSpace(result)) == 0 || bytes.Equal(bytes.TrimSpace(result), []byte("null")) {
		return fmt.Errorf("Moonraker extension response contains no result")
	}
	if err := json.Unmarshal(result, target); err != nil {
		return fmt.Errorf("decode Moonraker extension result: %w", err)
	}
	return nil
}

// RunGcode submits a script through Moonraker.
func (c *Client) RunGcode(ctx context.Context, script string) error {
	script = strings.TrimSpace(script)
	if script == "" {
		return fmt.Errorf("Moonraker G-code script is empty")
	}
	payload, err := json.Marshal(struct {
		Script string `json:"script"`
	}{Script: script})
	if err != nil {
		return fmt.Errorf("encode Moonraker G-code request: %w", err)
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		c.baseURL+"/printer/gcode/script",
		bytes.NewReader(payload),
	)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		request.Header.Set("X-Api-Key", c.apiKey)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read Moonraker G-code response: %w", err)
	}
	var envelope struct {
		Error *rpcError `json:"error"`
	}
	decoded := json.Unmarshal(body, &envelope) == nil
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if decoded && envelope.Error != nil {
			return fmt.Errorf("Moonraker %s: %w", response.Status, envelope.Error)
		}
		return fmt.Errorf("Moonraker %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	if !decoded {
		return fmt.Errorf("decode Moonraker G-code response: invalid JSON")
	}
	if envelope.Error != nil {
		return envelope.Error
	}
	return nil
}

func (c *Client) get(ctx context.Context, path string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	if c.apiKey != "" {
		request.Header.Set("X-Api-Key", c.apiKey)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		return &HTTPError{StatusCode: response.StatusCode, Detail: strings.TrimSpace(string(body))}
	}
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		return fmt.Errorf("decode Moonraker response: %w", err)
	}
	return nil
}

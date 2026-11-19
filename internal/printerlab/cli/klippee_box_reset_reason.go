package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/cl-tim/homelab-cli/internal/output"
	"github.com/cl-tim/homelab-cli/internal/printerlab/inventory"
	"github.com/cl-tim/homelab-cli/internal/printerlab/moonraker"
)

const (
	klippeeBoxResetReasonMethod  = "klippee_query_box_reset_reason"
	klippeeBoxResetReasonCommand = "query-box-reset-reason"
	klippeeBoxResetReasonSchema  = "klippee-box-reset-reason/v2"
	klippeeBoxResetReasonMCU     = "mcu_box1"
	klippeeBoxResetReasonOID     = 0xA5
	klippeeBoxResetReasonCaveat  = "RCC_CSR reset flags are sticky until RMVF is cleared; this read-only query does not clear them and they may predate the incident."
	klippeeBoxResetReasonRCCAddr = "0x40023874"
	klippeeBoxResetReasonRCCOrd  = 2

	defaultKlippeeBoxResetReasonWait = 30 * time.Second
	maximumKlippeeBoxResetReasonWait = 2 * time.Minute
	maximumKlippeeStructuredResult   = 32 * 1024
)

var (
	boxResetReasonSerial             = regexp.MustCompile(`^[0-9A-F]{24}$`)
	boxResetReasonConfiguredEndpoint = regexp.MustCompile(
		`^/dev/serial/by-id/usb-Klipper_QIDI_BOX_V1_1\.1\.1_[0-9A-F]{24}(?:-if00)?$`,
	)
	boxResetReasonTTY = regexp.MustCompile(`^/dev/tty(?:ACM|USB)[0-9]{1,4}$`)
	boxResetReasons   = map[string]struct{}{
		"PowerOn": {}, "Unknown": {}, "LowPower": {},
		"WindowWatchdog": {}, "Software": {},
		"IndependentWatchdog": {}, "NRST_Pin": {},
	}
	boxResetFlagMasks = []struct {
		name string
		mask uint32
	}{
		{"LowPower", 0x80000000},
		{"WindowWatchdog", 0x40000000},
		{"IndependentWatchdog", 0x20000000},
		{"Software", 0x10000000},
		{"PowerOn", 0x08000000},
		{"NRST_Pin", 0x04000000},
		{"Brownout", 0x02000000},
	}
	boxResetReasonJSONShape = exactJSONObjectShape{
		"schema": nil, "target_mcu": nil, "reason": nil, "reset_flags": {
			"register": nil, "address": nil, "raw": nil, "raw_hex": nil,
			"asserted": nil,
		}, "queried_at": nil,
		"firmware": {"version": nil, "mcu": nil},
		"endpoint": {
			"configured": nil, "resolved": nil, "vendor_id": nil,
			"product_id": nil, "manufacturer": nil, "product": nil,
			"serial": nil,
		},
		"query": {
			"rcc_csr": {
				"command": nil, "response": nil, "address": nil, "order": nil,
			},
			"reset_reason": {
				"command": nil, "response": nil, "oid": nil,
			},
		},
		"preconditions": {
			"fresh_klippy_state": nil, "endpoint_unowned": nil,
			"exclusive_open": nil,
		},
		"safety": {
			"read_only": nil, "reset_requested": nil, "flags_cleared": nil,
			"config_changed": nil, "klipper_action": nil, "may_be_stale": nil,
		},
		"caveat": nil,
	}
)

type exactJSONObjectShape map[string]exactJSONObjectShape

type klippeeBoxResetReasonFirmware struct {
	Version string `json:"version" yaml:"version"`
	MCU     string `json:"mcu" yaml:"mcu"`
}

type klippeeBoxResetReasonEndpoint struct {
	Configured   string `json:"configured" yaml:"configured"`
	Resolved     string `json:"resolved" yaml:"resolved"`
	VendorID     string `json:"vendor_id" yaml:"vendor_id"`
	ProductID    string `json:"product_id" yaml:"product_id"`
	Manufacturer string `json:"manufacturer" yaml:"manufacturer"`
	Product      string `json:"product" yaml:"product"`
	Serial       string `json:"serial" yaml:"serial"`
}

type klippeeBoxResetFlags struct {
	Register string   `json:"register" yaml:"register"`
	Address  string   `json:"address" yaml:"address"`
	Raw      uint32   `json:"raw" yaml:"raw"`
	RawHex   string   `json:"raw_hex" yaml:"raw_hex"`
	Asserted []string `json:"asserted" yaml:"asserted"`
}

type klippeeBoxResetReasonProtocol struct {
	Command  string `json:"command" yaml:"command"`
	Response string `json:"response" yaml:"response"`
	OID      *int   `json:"oid" yaml:"oid"`
}

type klippeeBoxResetReasonRCCQuery struct {
	Command  string `json:"command" yaml:"command"`
	Response string `json:"response" yaml:"response"`
	Address  string `json:"address" yaml:"address"`
	Order    *int   `json:"order" yaml:"order"`
}

type klippeeBoxResetReasonQuery struct {
	RCCCSR      klippeeBoxResetReasonRCCQuery `json:"rcc_csr" yaml:"rcc_csr"`
	ResetReason klippeeBoxResetReasonProtocol `json:"reset_reason" yaml:"reset_reason"`
}

type klippeeBoxResetReasonPreconditions struct {
	FreshKlippyState string `json:"fresh_klippy_state" yaml:"fresh_klippy_state"`
	EndpointUnowned  *bool  `json:"endpoint_unowned" yaml:"endpoint_unowned"`
	ExclusiveOpen    *bool  `json:"exclusive_open" yaml:"exclusive_open"`
}

// Pointer booleans distinguish an explicit safety attestation from an omitted
// field that would otherwise decode to the same zero value.
type klippeeBoxResetReasonSafety struct {
	ReadOnly       *bool  `json:"read_only" yaml:"read_only"`
	ResetRequested *bool  `json:"reset_requested" yaml:"reset_requested"`
	FlagsCleared   *bool  `json:"flags_cleared" yaml:"flags_cleared"`
	ConfigChanged  *bool  `json:"config_changed" yaml:"config_changed"`
	KlipperAction  string `json:"klipper_action" yaml:"klipper_action"`
	MayBeStale     *bool  `json:"may_be_stale" yaml:"may_be_stale"`
}

type klippeeBoxResetReasonResult struct {
	Schema        string                             `json:"schema" yaml:"schema"`
	TargetMCU     string                             `json:"target_mcu" yaml:"target_mcu"`
	Reason        string                             `json:"reason" yaml:"reason"`
	ResetFlags    klippeeBoxResetFlags               `json:"reset_flags" yaml:"reset_flags"`
	QueriedAt     string                             `json:"queried_at" yaml:"queried_at"`
	Firmware      klippeeBoxResetReasonFirmware      `json:"firmware" yaml:"firmware"`
	Endpoint      klippeeBoxResetReasonEndpoint      `json:"endpoint" yaml:"endpoint"`
	Preconditions klippeeBoxResetReasonPreconditions `json:"preconditions" yaml:"preconditions"`
	Query         klippeeBoxResetReasonQuery         `json:"query" yaml:"query"`
	Safety        klippeeBoxResetReasonSafety        `json:"safety" yaml:"safety"`
	Caveat        string                             `json:"caveat" yaml:"caveat"`
}

type klippeeBoxResetReasonReport struct {
	Printer       string                             `json:"printer" yaml:"printer"`
	OperationID   string                             `json:"operationId" yaml:"operationId"`
	Status        string                             `json:"status" yaml:"status"`
	Schema        string                             `json:"schema" yaml:"schema"`
	TargetMCU     string                             `json:"targetMcu" yaml:"targetMcu"`
	Reason        string                             `json:"reason" yaml:"reason"`
	ResetFlags    klippeeBoxResetFlags               `json:"resetFlags" yaml:"resetFlags"`
	QueriedAt     string                             `json:"queriedAt" yaml:"queriedAt"`
	Firmware      klippeeBoxResetReasonFirmware      `json:"firmware" yaml:"firmware"`
	Endpoint      klippeeBoxResetReasonEndpoint      `json:"endpoint" yaml:"endpoint"`
	Preconditions klippeeBoxResetReasonPreconditions `json:"preconditions" yaml:"preconditions"`
	Query         klippeeBoxResetReasonQuery         `json:"query" yaml:"query"`
	Safety        klippeeBoxResetReasonSafety        `json:"safety" yaml:"safety"`
	Caveat        string                             `json:"caveat" yaml:"caveat"`
}

func newKlippeeBoxResetReasonCmd(a *app) *cobra.Command {
	var waitTimeout time.Duration
	command := &cobra.Command{
		Use:   "box-reset-reason PRINTER",
		Short: "Show the Box MCU reset reason",
		Long: "Query the mcu_box1 reset reason through the printer's Klippee agent\n" +
			"and wait for the result.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if waitTimeout <= 0 || waitTimeout > maximumKlippeeBoxResetReasonWait {
				return fmt.Errorf(
					"--wait-timeout must be greater than zero and at most %s",
					maximumKlippeeBoxResetReasonWait,
				)
			}
			printer, err := a.onePrinter(args[0])
			if err != nil {
				return err
			}
			report, err := a.runKlippeeBoxResetReason(
				cmd.Context(), printer, waitTimeout,
			)
			if err != nil {
				return err
			}
			return writeKlippeeBoxResetReason(a, report)
		},
	}
	command.Flags().DurationVar(
		&waitTimeout,
		"wait-timeout",
		defaultKlippeeBoxResetReasonWait,
		"maximum wait for the query",
	)
	return command
}

func (a *app) runKlippeeBoxResetReason(
	ctx context.Context,
	printer inventory.Printer,
	waitTimeout time.Duration,
) (klippeeBoxResetReasonReport, error) {
	client, err := moonraker.New(printer, a.timeout())
	if err != nil {
		return klippeeBoxResetReasonReport{}, fmt.Errorf("%s: %w", printer.Name, err)
	}
	status, err := requestKlippeeOperationStatus(ctx, client, "")
	if err != nil {
		return klippeeBoxResetReasonReport{}, fmt.Errorf(
			"%s: box reset-reason query requires current Klippee operation-status support: %w",
			printer.Name,
			err,
		)
	}
	if err := requireKlippeeOperationQuiescent(status); err != nil {
		return klippeeBoxResetReasonReport{}, fmt.Errorf(
			"%s: box reset-reason query blocked because agent quiescence was not proven: %w",
			printer.Name,
			err,
		)
	}
	response, err := a.requestPrinterKlippeeAccepted(
		ctx,
		printer,
		client,
		klippeeBoxResetReasonMethod,
		"Box MCU reset-reason query",
		[]string{klippeeBoxResetReasonCommand},
	)
	if err != nil {
		return klippeeBoxResetReasonReport{}, err
	}
	operationID := response.OperationID
	if operationID == "" || operationID != strings.TrimSpace(operationID) ||
		len(operationID) > 128 {
		return klippeeBoxResetReasonReport{}, fmt.Errorf(
			"%s: Box MCU reset-reason query was accepted without a valid exact operation ID; do not retry it",
			printer.Name,
		)
	}
	operation, err := waitForKlippeeOperation(ctx, client, operationID, waitTimeout)
	if err != nil {
		return klippeeBoxResetReasonReport{}, fmt.Errorf("%s: %w", printer.Name, err)
	}
	if operation.Method != klippeeBoxResetReasonMethod ||
		operation.Command != klippeeBoxResetReasonCommand {
		return klippeeBoxResetReasonReport{}, fmt.Errorf(
			"%s: operation %s reported method/command %q/%q instead of the Box MCU reset-reason query",
			printer.Name,
			operation.ID,
			operation.Method,
			operation.Command,
		)
	}
	if operation.Status != "succeeded" ||
		operation.ReturnCode == nil || *operation.ReturnCode != 0 {
		return klippeeBoxResetReasonReport{}, fmt.Errorf(
			"%s: Box MCU reset-reason operation %s completed with status %q, return code %s: %s",
			printer.Name,
			operation.ID,
			operation.Status,
			returnCodeLabel(operation.ReturnCode),
			operationSummary(operation),
		)
	}
	result, err := decodeKlippeeBoxResetReasonResult(operation.Result)
	if err != nil {
		return klippeeBoxResetReasonReport{}, fmt.Errorf(
			"%s: Box MCU reset-reason operation %s returned an invalid structured result: %w",
			printer.Name,
			operation.ID,
			err,
		)
	}
	if err := validateKlippeeBoxResetReasonOperationWindow(operation, result); err != nil {
		return klippeeBoxResetReasonReport{}, fmt.Errorf(
			"%s: Box MCU reset-reason operation %s returned inconsistent timestamps: %w",
			printer.Name,
			operation.ID,
			err,
		)
	}
	return klippeeBoxResetReasonReport{
		Printer: printer.Name, OperationID: operation.ID, Status: operation.Status,
		Schema: result.Schema, TargetMCU: result.TargetMCU, Reason: result.Reason,
		ResetFlags: result.ResetFlags,
		QueriedAt:  result.QueriedAt, Firmware: result.Firmware,
		Endpoint: result.Endpoint, Preconditions: result.Preconditions,
		Query: result.Query, Safety: result.Safety,
		Caveat: result.Caveat,
	}, nil
}

func validateKlippeeBoxResetReasonOperationWindow(
	operation *moonraker.ExtensionOperation,
	result klippeeBoxResetReasonResult,
) error {
	startedAt, err := time.Parse(time.RFC3339, operation.StartedAt)
	if err != nil {
		return fmt.Errorf("started_at is not RFC3339: %w", err)
	}
	completedAt, err := time.Parse(time.RFC3339, operation.CompletedAt)
	if err != nil {
		return fmt.Errorf("completed_at is not RFC3339: %w", err)
	}
	queriedAt, err := time.Parse(time.RFC3339, result.QueriedAt)
	if err != nil {
		return fmt.Errorf("queried_at is not RFC3339: %w", err)
	}
	if completedAt.Before(startedAt) {
		return fmt.Errorf("completed_at precedes started_at")
	}
	if queriedAt.Before(startedAt) || queriedAt.After(completedAt) {
		return fmt.Errorf("queried_at falls outside the exact operation window")
	}
	return nil
}

func decodeKlippeeBoxResetReasonResult(
	raw json.RawMessage,
) (klippeeBoxResetReasonResult, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return klippeeBoxResetReasonResult{}, fmt.Errorf("operation omitted its structured result")
	}
	if len(trimmed) > maximumKlippeeStructuredResult {
		return klippeeBoxResetReasonResult{}, fmt.Errorf(
			"structured result exceeds %d bytes", maximumKlippeeStructuredResult,
		)
	}
	if trimmed[0] != '{' {
		return klippeeBoxResetReasonResult{}, fmt.Errorf("structured result must be one JSON object")
	}
	if err := rejectDuplicateJSONNames(trimmed); err != nil {
		return klippeeBoxResetReasonResult{}, err
	}
	if err := requireExactJSONObjectShape(
		trimmed, "structured result", boxResetReasonJSONShape,
	); err != nil {
		return klippeeBoxResetReasonResult{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	var result klippeeBoxResetReasonResult
	if err := decoder.Decode(&result); err != nil {
		return klippeeBoxResetReasonResult{}, fmt.Errorf("decode schema result: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return klippeeBoxResetReasonResult{}, err
	}
	if err := validateKlippeeBoxResetReasonResult(result); err != nil {
		return klippeeBoxResetReasonResult{}, err
	}
	return result, nil
}

func requireExactJSONObjectShape(
	raw json.RawMessage,
	path string,
	shape exactJSONObjectShape,
) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		if err != nil {
			return fmt.Errorf("%s must be a JSON object: %w", path, err)
		}
		return fmt.Errorf("%s must be a JSON object", path)
	}
	for name := range object {
		if _, allowed := shape[name]; !allowed {
			return fmt.Errorf("%s contains unexpected exact-case field %q", path, name)
		}
	}
	for name, childShape := range shape {
		child, present := object[name]
		if !present {
			return fmt.Errorf("%s omitted required field %q", path, name)
		}
		if childShape != nil {
			if err := requireExactJSONObjectShape(
				child, path+"."+name, childShape,
			); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateKlippeeBoxResetReasonResult(result klippeeBoxResetReasonResult) error {
	if result.Schema != klippeeBoxResetReasonSchema {
		return fmt.Errorf(
			"schema is %q; expected %q", result.Schema, klippeeBoxResetReasonSchema,
		)
	}
	if result.TargetMCU != klippeeBoxResetReasonMCU {
		return fmt.Errorf(
			"target MCU is %q; expected %q", result.TargetMCU, klippeeBoxResetReasonMCU,
		)
	}
	if _, ok := boxResetReasons[result.Reason]; !ok {
		return fmt.Errorf("reset reason %q is not in the v2 schema", result.Reason)
	}
	resetFlags := result.ResetFlags
	if resetFlags.Register != "RCC_CSR" ||
		resetFlags.Address != klippeeBoxResetReasonRCCAddr ||
		resetFlags.RawHex != fmt.Sprintf("0x%08X", resetFlags.Raw) {
		return fmt.Errorf("raw reset-flags proof does not identify a consistent RCC_CSR value")
	}
	if resetFlags.Asserted == nil {
		return fmt.Errorf("raw reset-flags proof omitted its asserted flag list")
	}
	expectedFlags := make([]string, 0, len(boxResetFlagMasks))
	for _, flag := range boxResetFlagMasks {
		if resetFlags.Raw&flag.mask != 0 {
			expectedFlags = append(expectedFlags, flag.name)
		}
	}
	if len(resetFlags.Asserted) != len(expectedFlags) {
		return fmt.Errorf("raw reset-flags proof is inconsistent with RCC_CSR")
	}
	for index := range expectedFlags {
		if resetFlags.Asserted[index] != expectedFlags[index] {
			return fmt.Errorf("raw reset-flags proof is inconsistent with RCC_CSR")
		}
	}
	queriedAt, err := time.Parse(time.RFC3339, result.QueriedAt)
	if err != nil {
		return fmt.Errorf("queried_at is not an RFC3339 timestamp: %w", err)
	}
	_, utcOffset := queriedAt.Zone()
	if utcOffset != 0 {
		return fmt.Errorf("queried_at is not UTC")
	}
	if result.Firmware.Version != "KLP_MCU_MKS_V1_1.1.1" ||
		result.Firmware.MCU != "stm32f401xc" {
		return fmt.Errorf(
			"firmware identity is %q/%q; expected the reviewed Box MCU image",
			result.Firmware.Version,
			result.Firmware.MCU,
		)
	}
	endpoint := result.Endpoint
	if endpoint.VendorID != "1d50" || endpoint.ProductID != "614e" ||
		endpoint.Manufacturer != "Klipper" || endpoint.Product != "QIDI_BOX_V1_1.1.1" {
		return fmt.Errorf("USB endpoint identity does not match the reviewed Box MCU")
	}
	if !boxResetReasonSerial.MatchString(endpoint.Serial) {
		return fmt.Errorf("USB endpoint serial is not a 24-digit hexadecimal STM32 serial")
	}
	if !boxResetReasonConfiguredEndpoint.MatchString(endpoint.Configured) ||
		!strings.Contains(strings.ToUpper(endpoint.Configured), strings.ToUpper(endpoint.Serial)) {
		return fmt.Errorf("configured endpoint is not the reviewed stable Box by-id path for the reported serial")
	}
	if !boxResetReasonTTY.MatchString(endpoint.Resolved) {
		return fmt.Errorf("resolved endpoint is not a bounded USB serial TTY path")
	}
	preconditions := result.Preconditions
	if preconditions.FreshKlippyState != "shutdown" {
		return fmt.Errorf(
			"precondition reported fresh Klippy state %q; expected shutdown",
			preconditions.FreshKlippyState,
		)
	}
	if preconditions.EndpointUnowned == nil || !*preconditions.EndpointUnowned {
		return fmt.Errorf("precondition did not explicitly attest endpoint_unowned=true")
	}
	if preconditions.ExclusiveOpen == nil || !*preconditions.ExclusiveOpen {
		return fmt.Errorf("precondition did not explicitly attest exclusive_open=true")
	}
	if result.Query.RCCCSR.Command != "debug_read order=%c addr=%u" ||
		result.Query.RCCCSR.Response != "debug_result val=%u" ||
		result.Query.RCCCSR.Address != klippeeBoxResetReasonRCCAddr ||
		result.Query.RCCCSR.Order == nil ||
		*result.Query.RCCCSR.Order != klippeeBoxResetReasonRCCOrd {
		return fmt.Errorf("query proof does not match the reviewed RCC_CSR read protocol")
	}
	if result.Query.ResetReason.Command != "reset_reason_get oid=%c" ||
		result.Query.ResetReason.Response != "reset_reason_return oid=%c reason=%s" ||
		result.Query.ResetReason.OID == nil ||
		*result.Query.ResetReason.OID != klippeeBoxResetReasonOID {
		return fmt.Errorf("query proof does not match the reviewed reset-reason protocol")
	}
	safety := result.Safety
	if safety.ReadOnly == nil || !*safety.ReadOnly {
		return fmt.Errorf("safety proof did not explicitly attest read_only=true")
	}
	if safety.ResetRequested == nil || *safety.ResetRequested {
		return fmt.Errorf("safety proof did not explicitly attest reset_requested=false")
	}
	if safety.FlagsCleared == nil || *safety.FlagsCleared {
		return fmt.Errorf("safety proof did not explicitly attest flags_cleared=false")
	}
	if safety.ConfigChanged == nil || *safety.ConfigChanged {
		return fmt.Errorf("safety proof did not explicitly attest config_changed=false")
	}
	if safety.KlipperAction != "none" {
		return fmt.Errorf("safety proof reported Klipper action %q; expected none", safety.KlipperAction)
	}
	if safety.MayBeStale == nil || !*safety.MayBeStale {
		return fmt.Errorf("safety proof omitted the required may_be_stale caveat")
	}
	if result.Caveat != klippeeBoxResetReasonCaveat {
		return fmt.Errorf("result omitted the exact retained-flags caveat")
	}
	return nil
}

func rejectDuplicateJSONNames(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := consumeUniqueJSONValue(decoder); err != nil {
		return err
	}
	return requireJSONEOF(decoder)
}

func consumeUniqueJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("decode structured result: %w", err)
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
				return fmt.Errorf("decode structured result field: %w", err)
			}
			name, ok := nameToken.(string)
			if !ok {
				return fmt.Errorf("structured result object contains a non-string field name")
			}
			if _, duplicate := seen[name]; duplicate {
				return fmt.Errorf("structured result contains duplicate field %q", name)
			}
			seen[name] = struct{}{}
			if err := consumeUniqueJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return fmt.Errorf("structured result has an unterminated object")
		}
	case '[':
		for decoder.More() {
			if err := consumeUniqueJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return fmt.Errorf("structured result has an unterminated array")
		}
	default:
		return fmt.Errorf("structured result contains unexpected delimiter %q", delimiter)
	}
	return nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("decode trailing structured result: %w", err)
	}
	return fmt.Errorf("structured result contains more than one JSON value")
}

func writeKlippeeBoxResetReason(a *app, report klippeeBoxResetReasonReport) error {
	table := output.Table{Headers: []string{
		"PRINTER", "OPERATION", "MCU", "RESET REASON", "RESET FLAGS", "QUERIED AT",
		"PRECONDITIONS", "ACTIONS", "MAY BE STALE",
	}}
	table.AddRow(
		report.Printer,
		report.OperationID,
		report.TargetMCU,
		report.Reason,
		fmt.Sprintf("%s; %s", report.ResetFlags.RawHex, strings.Join(report.ResetFlags.Asserted, ",")),
		report.QueriedAt,
		fmt.Sprintf(
			"Klippy=%s; endpoint-unowned=%t; exclusive-open=%t",
			report.Preconditions.FreshKlippyState,
			*report.Preconditions.EndpointUnowned,
			*report.Preconditions.ExclusiveOpen,
		),
		fmt.Sprintf(
			"read-only=%t; reset=%t; flags-cleared=%t; config=%t; Klipper=%s",
			*report.Safety.ReadOnly,
			*report.Safety.ResetRequested,
			*report.Safety.FlagsCleared,
			*report.Safety.ConfigChanged,
			report.Safety.KlipperAction,
		),
		*report.Safety.MayBeStale,
	)
	return output.Write(a.out, a.format, report, table)
}

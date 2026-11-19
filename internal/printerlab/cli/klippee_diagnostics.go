package cli

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/cl-tim/homelab-cli/internal/output"
	"github.com/cl-tim/homelab-cli/internal/printerlab/inventory"
	"github.com/cl-tim/homelab-cli/internal/printerlab/migrate"
	"github.com/cl-tim/homelab-cli/internal/printerlab/moonraker"
)

const (
	klippeeDiagnosticsMethod        = "klippee_upload_diagnostics"
	klippeeDiagnosticsCommand       = "upload-diagnostics"
	klippeeDiagnosticsURLMarker     = "klippee: open "
	klippeeDiagnosticsSuccessMarker = "klippee: DIAGNOSTIC UPLOAD SUCCESSFUL"
	klippeeDiagnosticsPrivacyMarker = "klippee: diagnostic bundle uploaded privately for 48 hours"
	maximumKlippeeDiagnosticsWait   = 10 * time.Minute
)

var diagnosticUploadID = regexp.MustCompile(
	`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-4[0-9a-fA-F]{3}-[89aAbB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`,
)

type klippeeDiagnosticsReport struct {
	Printer     string `json:"printer" yaml:"printer"`
	Transport   string `json:"transport" yaml:"transport"`
	OperationID string `json:"operationId,omitempty" yaml:"operationId,omitempty"`
	Status      string `json:"status" yaml:"status"`
	Private     bool   `json:"private" yaml:"private"`
	Retention   string `json:"retention" yaml:"retention"`
	PrivateURL  string `json:"privateUrl" yaml:"privateUrl"`
	Result      string `json:"result" yaml:"result"`
}

func newKlippeeDiagnosticsCmd(a *app) *cobra.Command {
	var useSSH bool
	var previous bool
	var waitTimeout time.Duration
	command := &cobra.Command{
		Use:   "diagnostics PRINTER",
		Short: "Upload printer diagnostics and return a private link",
		Long: "Collect and upload diagnostics through the printer's Klippee agent,\n" +
			"then print a private URL valid for 48 hours. Use --ssh to upload through SSH.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if waitTimeout <= 0 || waitTimeout > maximumKlippeeDiagnosticsWait {
				return fmt.Errorf(
					"--wait-timeout must be greater than zero and at most %s",
					maximumKlippeeDiagnosticsWait,
				)
			}
			printer, err := a.onePrinter(args[0])
			if err != nil {
				return err
			}

			var report klippeeDiagnosticsReport
			if previous && !useSSH {
				return fmt.Errorf("--previous requires --ssh until the printer agent supports rotated-log selection")
			}
			if useSSH {
				report, err = a.runKlippeeDiagnosticsSSH(
					cmd.Context(), printer, waitTimeout, previous,
				)
			} else {
				report, err = a.runKlippeeDiagnosticsExtension(
					cmd.Context(), printer, waitTimeout,
				)
			}
			if err != nil {
				return err
			}
			return writeKlippeeDiagnostics(a, report)
		},
	}
	command.Flags().BoolVar(
		&useSSH,
		"ssh",
		false,
		"upload through SSH",
	)
	command.Flags().BoolVar(
		&previous,
		"previous",
		false,
		"upload the newest rotated klippy.log (requires --ssh)",
	)
	command.Flags().DurationVar(
		&waitTimeout,
		"wait-timeout",
		defaultKlippeeWaitTimeout,
		"maximum wait for collection and upload",
	)
	return command
}

func (a *app) runKlippeeDiagnosticsExtension(
	ctx context.Context,
	printer inventory.Printer,
	waitTimeout time.Duration,
) (klippeeDiagnosticsReport, error) {
	client, err := moonraker.New(printer, a.timeout())
	if err != nil {
		return klippeeDiagnosticsReport{}, fmt.Errorf("%s: %w", printer.Name, err)
	}
	status, err := requestKlippeeOperationStatus(ctx, client, "")
	if err != nil {
		return klippeeDiagnosticsReport{}, fmt.Errorf(
			"%s: diagnostics requires current Klippee operation-status support; "+
				"use --ssh only as an explicit fallback: %w",
			printer.Name,
			err,
		)
	}
	if err := requireKlippeeOperationQuiescent(status); err != nil {
		return klippeeDiagnosticsReport{}, fmt.Errorf(
			"%s: diagnostics blocked because agent quiescence was not proven: %w",
			printer.Name,
			err,
		)
	}
	response, err := a.requestPrinterKlippeeAccepted(
		ctx,
		printer,
		client,
		klippeeDiagnosticsMethod,
		"diagnostics upload",
		[]string{klippeeDiagnosticsCommand},
	)
	if err != nil {
		return klippeeDiagnosticsReport{}, err
	}
	operationID := strings.TrimSpace(response.OperationID)
	if operationID == "" || len(operationID) > 128 {
		return klippeeDiagnosticsReport{}, fmt.Errorf(
			"%s: diagnostics upload was accepted without a valid operation ID; "+
				"completion and its private URL cannot be verified",
			printer.Name,
		)
	}
	operation, err := waitForKlippeeOperation(ctx, client, operationID, waitTimeout)
	if err != nil {
		return klippeeDiagnosticsReport{}, fmt.Errorf("%s: %w", printer.Name, err)
	}
	if operation.Method != klippeeDiagnosticsMethod ||
		operation.Command != klippeeDiagnosticsCommand {
		return klippeeDiagnosticsReport{}, fmt.Errorf(
			"%s: operation %s reported method/command %q/%q instead of diagnostics",
			printer.Name,
			operation.ID,
			operation.Method,
			operation.Command,
		)
	}
	if operation.Status != "succeeded" ||
		operation.ReturnCode == nil || *operation.ReturnCode != 0 {
		return klippeeDiagnosticsReport{}, fmt.Errorf(
			"%s: diagnostics operation %s completed with status %q, return code %s: %s",
			printer.Name,
			operation.ID,
			operation.Status,
			returnCodeLabel(operation.ReturnCode),
			redactKlippeeDiagnosticsURL(migrate.SafeDiagnosticSummary([]byte(operation.Summary))),
		)
	}
	report, err := klippeeDiagnosticsReportFromResult(
		printer.Name, "moonraker-extension", operation.ID, operation.Summary,
	)
	if err != nil {
		return klippeeDiagnosticsReport{}, fmt.Errorf(
			"%s: diagnostics operation %s returned an invalid result: %w",
			printer.Name,
			operation.ID,
			err,
		)
	}
	return report, nil
}

func (a *app) runKlippeeDiagnosticsSSH(
	ctx context.Context,
	printer inventory.Printer,
	waitTimeout time.Duration,
	previous bool,
) (klippeeDiagnosticsReport, error) {
	waitContext, cancel := context.WithTimeout(ctx, waitTimeout)
	defer cancel()
	runner := a.migrationRunner()
	var raw []byte
	var err error
	if previous {
		raw, err = runner.KlippeePreviousDiagnosticsSSHOutput(
			waitContext, printer, a.timeout(),
		)
	} else {
		raw, err = runner.KlippeeDiagnosticsSSHOutput(
			waitContext, printer, a.timeout(),
		)
	}
	if err != nil {
		if waitContext.Err() == context.DeadlineExceeded {
			return klippeeDiagnosticsReport{}, fmt.Errorf(
				"%s: SSH diagnostics timed out after %s; it was not retried: %w",
				printer.Name,
				waitTimeout,
				waitContext.Err(),
			)
		}
		return klippeeDiagnosticsReport{}, fmt.Errorf(
			"%s: guarded SSH diagnostics failed: %w", printer.Name, err,
		)
	}
	transport := "ssh"
	if previous {
		transport = "ssh-previous"
	}
	report, err := klippeeDiagnosticsReportFromResult(
		printer.Name, transport, "", string(raw),
	)
	if err != nil {
		return klippeeDiagnosticsReport{}, fmt.Errorf(
			"%s: guarded SSH diagnostics returned an invalid result: %w",
			printer.Name,
			err,
		)
	}
	return report, nil
}

func klippeeDiagnosticsReportFromResult(
	printer, transport, operationID, raw string,
) (klippeeDiagnosticsReport, error) {
	result := migrate.SafeDiagnosticSummary([]byte(raw))
	if !strings.Contains(result, klippeeDiagnosticsPrivacyMarker) ||
		!strings.Contains(result, klippeeDiagnosticsSuccessMarker) {
		return klippeeDiagnosticsReport{}, fmt.Errorf(
			"result omitted the private-upload success proof: %s",
			redactKlippeeDiagnosticsURL(result),
		)
	}
	privateURL, err := extractKlippeeDiagnosticsURL(result)
	if err != nil {
		return klippeeDiagnosticsReport{}, err
	}
	redactedResult := redactKlippeeDiagnosticsURL(result)
	redactedResult = strings.ReplaceAll(redactedResult, privateURL, "[private URL]")
	return klippeeDiagnosticsReport{
		Printer: printer, Transport: transport, OperationID: operationID,
		Status: "succeeded", Private: true, Retention: "48 hours",
		PrivateURL: privateURL, Result: redactedResult,
	}, nil
}

func extractKlippeeDiagnosticsURL(result string) (string, error) {
	if strings.Count(result, klippeeDiagnosticsURLMarker) != 1 {
		return "", fmt.Errorf("result must contain exactly one private diagnostics URL")
	}
	after, _ := strings.CutPrefix(
		result[strings.Index(result, klippeeDiagnosticsURLMarker):],
		klippeeDiagnosticsURLMarker,
	)
	fields := strings.Fields(after)
	if len(fields) == 0 {
		return "", fmt.Errorf("private diagnostics URL is empty")
	}
	candidate := fields[0]
	if len(candidate) > 2048 {
		return "", fmt.Errorf("private diagnostics URL exceeds 2048 bytes")
	}
	parsed, err := url.ParseRequestURI(candidate)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" ||
		parsed.User != nil || parsed.Opaque != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("private diagnostics URL is not an absolute credential-free HTTPS URL")
	}
	if parsed.Path != "/log-diagnostics" ||
		(parsed.RawPath != "" && parsed.EscapedPath() != "/log-diagnostics") {
		return "", fmt.Errorf("private diagnostics URL has an unexpected path")
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil || len(query) != 1 {
		return "", fmt.Errorf("private diagnostics URL has an invalid query")
	}
	values, ok := query["upload"]
	if !ok || len(values) != 1 || !diagnosticUploadID.MatchString(values[0]) {
		return "", fmt.Errorf("private diagnostics URL has an invalid upload ID")
	}
	return candidate, nil
}

func redactKlippeeDiagnosticsURL(result string) string {
	var redacted strings.Builder
	for {
		index := strings.Index(result, klippeeDiagnosticsURLMarker)
		if index < 0 {
			redacted.WriteString(result)
			return redacted.String()
		}
		start := index + len(klippeeDiagnosticsURLMarker)
		end := start
		for end < len(result) && result[end] != ' ' {
			end++
		}
		redacted.WriteString(result[:start])
		redacted.WriteString("[private URL]")
		result = result[end:]
	}
}

func writeKlippeeDiagnostics(a *app, report klippeeDiagnosticsReport) error {
	table := output.Table{Headers: []string{
		"PRINTER", "TRANSPORT", "STATUS", "PRIVATE", "RETENTION", "PRIVATE URL", "RESULT",
	}}
	table.AddRow(
		report.Printer,
		report.Transport,
		report.Status,
		report.Private,
		report.Retention,
		report.PrivateURL,
		report.Result,
	)
	return output.Write(a.out, a.format, report, table)
}

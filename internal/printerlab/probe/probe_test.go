package probe

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cl-tim/hlab/internal/printerlab/inventory"
)

type stubRunner struct {
	out  string
	err  error
	args []string
}

func (s *stubRunner) Output(_ context.Context, args ...string) ([]byte, error) {
	s.args = args
	return []byte(s.out), s.err
}

func testPrinter() inventory.Printer {
	return inventory.Printer{
		Name: "printer-a", Address: "plus4.example", User: "mks", Port: 22,
	}
}

const reachableReport = `{
  "schema": 1,
  "printer": "printer-a",
  "reachable": true,
  "probes": [
    {"probe": "bootstrap.receipt", "stage": "bootstrap-host",
     "status": "satisfied", "detail": "host bootstrap receipt present"},
    {"probe": "mcu.online", "stage": "flash-toolhead",
     "status": "indeterminate", "detail": "Moonraker did not answer"}
  ],
  "summary": {"satisfied": 1, "indeterminate": 1, "unsatisfied": 0}
}`

func TestCollectParsesAReport(t *testing.T) {
	runner := &stubRunner{out: reachableReport}

	report, err := Client{Runner: runner}.Collect(context.Background(), testPrinter())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}

	if !report.Reachable {
		t.Error("expected a reachable report")
	}
	statuses := report.Statuses()
	if statuses["bootstrap.receipt"] != Satisfied {
		t.Errorf("got %q, want satisfied", statuses["bootstrap.receipt"])
	}
	if statuses["mcu.online"] != Indeterminate {
		t.Errorf("got %q, want indeterminate", statuses["mcu.online"])
	}
	if detail := report.Details()["mcu.online"]; !strings.Contains(detail, "Moonraker") {
		t.Errorf("probe detail was lost: %q", detail)
	}
}

func TestCollectPassesConnectionArguments(t *testing.T) {
	runner := &stubRunner{out: reachableReport}

	if _, err := (Client{Runner: runner}).Collect(context.Background(), testPrinter()); err != nil {
		t.Fatalf("collect: %v", err)
	}

	if runner.args[0] != "probe" {
		t.Fatalf("got subcommand %q, want probe", runner.args[0])
	}
	joined := strings.Join(runner.args, " ")
	for _, want := range []string{"--printer plus4.example", "--user mks", "--port 22"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %q", want, joined)
		}
	}
}

// An unreachable printer is reported inside the document with exit 0, so it
// must arrive here as data rather than as an error.
func TestUnreachableReportIsNotAnError(t *testing.T) {
	runner := &stubRunner{out: `{
      "schema": 1, "printer": "printer-a", "reachable": false,
      "probes": [{"probe": "mcu.online", "stage": "flash-toolhead",
                  "status": "indeterminate", "detail": "ssh exited 255"}]
    }`}

	report, err := Client{Runner: runner}.Collect(context.Background(), testPrinter())
	if err != nil {
		t.Fatalf("an unreachable printer must not be an error: %v", err)
	}
	if report.Reachable {
		t.Error("expected reachable to be false")
	}
	if report.Statuses()["mcu.online"] != Indeterminate {
		t.Error("expected every probe to be indeterminate")
	}
}

func TestToolFailureIsAnError(t *testing.T) {
	runner := &stubRunner{err: errors.New("migration stage failed with exit code 2")}

	if _, err := (Client{Runner: runner}).Collect(context.Background(), testPrinter()); err == nil {
		t.Fatal("expected a tooling failure to surface as an error")
	}
}

func TestMalformedOutputIsAnError(t *testing.T) {
	runner := &stubRunner{out: "not json at all"}

	if _, err := (Client{Runner: runner}).Collect(context.Background(), testPrinter()); err == nil {
		t.Fatal("expected a parse error")
	}
}

// A firmware repository emitting a schema this build does not understand must
// not be silently reinterpreted.
func TestUnknownSchemaIsRejected(t *testing.T) {
	runner := &stubRunner{out: `{"schema": 7, "reachable": true, "probes": []}`}

	_, err := Client{Runner: runner}.Collect(context.Background(), testPrinter())
	if err == nil || !strings.Contains(err.Error(), "schema") {
		t.Fatalf("got %v, want an unsupported schema error", err)
	}
}

package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func runFleetCommand(path string, args ...string) (string, error) {
	var out, errOut bytes.Buffer
	cmd := NewCommand(strings.NewReader(""), &out, &errOut)
	cmd.SetArgs(append([]string{"--config", path, "-o", "json"}, args...))
	err := cmd.Execute()
	return out.String(), err
}

func TestFleetJobHistoryAndSpoolman(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method %s", r.Method)
		}
		switch r.URL.Path {
		case "/server/history/list":
			query := r.URL.Query()
			since, err := strconv.ParseFloat(query.Get("since"), 64)
			if err != nil || query.Get("limit") != "25" || query.Get("order") != "desc" ||
				time.Since(time.Unix(int64(since), 0).Add(7*24*time.Hour)).Abs() > time.Minute {
				t.Errorf("unexpected history query %s", query.Encode())
			}
			fmt.Fprint(w, `{"result":{"jobs":[{"job_id":"42","filename":"part.gcode","status":"completed","start_time":1700000000.5,"print_duration":3600,"filament_used":654.2,"auxiliary_data":[{"provider":"spoolman","name":"spool_ids","value":[6,9]}]},{"job_id":"41","status":"cancelled","start_time":1699990000}]}}`)
		case "/server/spoolman/status":
			fmt.Fprint(w, `{"result":{"spoolman_connected":false,"spool_id":9,"pending_reports":[{"spool_id":6,"filament_used":123}]}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	// The second printer has Moonraker without the Spoolman endpoint, and
	// history is inaccessible. Both reports must preserve the healthy result.
	other := httptest.NewServer(http.HandlerFunc(http.NotFound))
	defer other.Close()
	path := componentConfig(t, server.URL)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(raw), "name: other\n    address: other.invalid\n    moonrakerURL: \""+server.URL+"\"", "name: other\n    address: other.invalid\n    moonrakerURL: \""+other.URL+"\"", 1)
	if err := os.WriteFile(path, []byte(updated), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := runFleetCommand(path, "jobs", "history", "@workshop", "--since", "7d", "--limit", "25")
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 1 {
		t.Fatalf("expected partial failure, got %v", err)
	}
	var history []jobHistoryReport
	if err := json.Unmarshal([]byte(out), &history); err != nil || len(history) != 2 || len(history[0].Jobs) != 2 || history[1].Error == "" {
		t.Fatalf("invalid history %s (%v)", out, err)
	}
	first, second := history[0].Jobs[0], history[0].Jobs[1]
	if first.StartedAt.UnixMilli() != 1700000000500 || fmt.Sprint(first.SpoolIDs) != "[6 9]" || len(second.SpoolIDs) != 0 || second.Status != "cancelled" {
		t.Fatalf("incorrect history %s", out)
	}

	out, err = runFleetCommand(path, "spoolman", "status", "@workshop")
	if err != nil {
		t.Fatal(err)
	}
	var spools []spoolmanReport
	if err := json.Unmarshal([]byte(out), &spools); err != nil || len(spools) != 2 ||
		spools[0].State != "disconnected" || spools[0].Spoolman == nil || spools[1].State != "unavailable" {
		t.Fatalf("invalid Spoolman result %s (%v)", out, err)
	}
	status := spools[0].Spoolman
	if status.ActiveSpoolID == nil || *status.ActiveSpoolID != 9 || len(status.PendingReports) != 1 ||
		status.PendingReports[0].SpoolID != 6 || status.PendingReports[0].FilamentUsedMM != 123 {
		t.Fatalf("incorrect Spoolman usage %s", out)
	}
}

func TestComponentMaintenanceLifecycle(t *testing.T) {
	var total atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/server/history/totals" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		fmt.Fprintf(w, `{"result":{"job_totals":{"total_print_time":%d}}}`, total.Load())
	}))
	defer server.Close()
	path := componentConfig(t, server.URL)
	for _, args := range [][]string{
		{"components", "set", "orange", "hotend.upgrade", "--status", "installed"},
		{"maintenance", "set", "orange", "hotend.upgrade", "--interval-hours", "2"},
	} {
		if _, err := runFleetCommand(path, args...); err != nil {
			t.Fatal(err)
		}
	}
	check := func(wantState string, wantHours *float64, wantError bool) {
		t.Helper()
		out, err := runFleetCommand(path, "maintenance", "due", "#voron")
		if (err != nil) != wantError {
			t.Fatalf("unexpected exit for %s: %v", wantState, err)
		}
		var reports []maintenanceReport
		if err := json.Unmarshal([]byte(out), &reports); err != nil || len(reports) != 1 || reports[0].State != wantState {
			t.Fatalf("want %s, got %s (%v)", wantState, out, err)
		}
		hours := reports[0].PrintHoursElapsed
		if wantHours == nil && hours != nil || wantHours != nil && (hours == nil || *wantHours != *hours || reports[0].LastServicedAt == nil) {
			t.Fatalf("unexpected usage: %s", out)
		}
	}
	check("unrecorded", nil, false)
	total.Store(3600)
	if _, err := runFleetCommand(path, "maintenance", "record", "orange", "hotend.upgrade"); err != nil {
		t.Fatal(err)
	}
	total.Store(10799)
	belowInterval := 7199.0 / 3600
	check("ok", &belowInterval, false)
	total.Store(10800)
	atInterval := 2.0
	check("due", &atInterval, false)
	total.Store(0)
	check("unknown", nil, true)
	if _, err := runFleetCommand(path, "maintenance", "record", "orange", "hotend.upgrade"); err != nil {
		t.Fatal(err)
	}
	zero := 0.0
	check("ok", &zero, false)
	raw, err := os.ReadFile(path)
	if err != nil || !bytes.Contains(raw, []byte("# Keep evidence")) || !bytes.Contains(raw, []byte("baselinePrintSeconds: 0")) {
		t.Fatalf("expected a saved zero baseline and the component comment: %s (%v)", raw, err)
	}
}

package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cl-tim/hlab/internal/printerlab/inventory"
)

func TestIdleAcknowledgementCannotOverrideAuthoritativeFailures(t *testing.T) {
	for _, tc := range []struct {
		name, state string
		code        int
		allowed     bool
	}{
		{"printing", "printing", 200, false},
		{"paused", "paused", 200, false},
		{"malformed", "", 200, false},
		{"unauthorized", "", 401, false},
		{"forbidden", "", 403, false},
		{"disconnected", "", 503, true},
		{"idle", "standby", 200, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/server/info" {
					fmt.Fprint(w, `{"result":{"klippy_connected":false,"klippy_state":"disconnected"}}`)
					return
				}
				w.WriteHeader(tc.code)
				fmt.Fprintf(w, `{"result":{"status":{"print_stats":{"state":%q}}}}`, tc.state)
			}))
			defer server.Close()
			a := promptApp("yes\n")
			p := inventory.Printer{Name: "test", Address: "test", MoonrakerURL: server.URL}
			err := a.ensureIdle(context.Background(), p, true)
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v, error=%v", tc.allowed, err)
			}
		})
	}
}

func TestResumeIdleAcknowledgementIsInteractive(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/server/info" {
			fmt.Fprint(w, `{"result":{"klippy_connected":false,"klippy_state":"disconnected"}}`)
		} else {
			w.WriteHeader(503)
		}
	}))
	defer server.Close()
	p := inventory.Printer{Name: "test", Address: "test", MoonrakerURL: server.URL}
	if err := promptApp("yes\n").ensureTransitionIdle(context.Background(), p, false); err == nil {
		t.Fatal("non-interactive resume accepted physical acknowledgement")
	}
	if err := promptApp("no\n").ensureTransitionIdle(context.Background(), p, true); err == nil {
		t.Fatal("resume accepted a declined acknowledgement")
	}
	if err := promptApp("yes\n").ensureTransitionIdle(context.Background(), p, true); err != nil {
		t.Fatal(err)
	}
}

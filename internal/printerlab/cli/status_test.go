package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStatusFailsAndShowsKlippyShutdownMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/server/info":
			_, _ = w.Write([]byte(`{"result":{"klippy_connected":true,"klippy_state":"shutdown"}}`))
		case "/printer/info":
			_, _ = w.Write([]byte(`{"result":{"state":"shutdown","state_message":"MCU mcu_box1 shutdown: Timer too close"}}`))
		case "/printer/objects/query":
			_, _ = w.Write([]byte(`{"result":{"status":{"print_stats":{"state":"standby"}}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	command := NewCommand(strings.NewReader(""), &stdout, &stderr)
	command.SetArgs([]string{
		"--config", klippeePhysicalConfig(t, server.URL), "status", "printer-a",
	})
	err := command.Execute()
	if err == nil {
		t.Fatal("status succeeded for shutdown Klippy")
	}
	if !strings.Contains(stdout.String(), "Klippy state is shutdown: MCU mcu_box1 shutdown: Timer too close") {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

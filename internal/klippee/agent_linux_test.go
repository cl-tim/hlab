package klippee

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cl-tim/hlab/internal/printerlab/moonraker"
)

func TestNativeAgentUsesFreshStateAndPreservesOperationContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), "moonraker.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	t.Setenv("KLIPPEE_MOONRAKER_SOCKET", path)
	var idle atomic.Bool
	var queries atomic.Int32
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				data := make([]byte, 4096)
				n, err := conn.Read(data)
				if err != nil || n == 0 {
					return
				}
				var msg moonrakerMessage
				if err := json.Unmarshal(data[:n-1], &msg); err != nil {
					t.Error(err)
					return
				}
				response := map[string]any{}
				if msg.Method == "printer.objects.query" {
					queries.Add(1)
					state := "printing"
					if idle.Load() {
						state = "standby"
					}
					response["status"] = map[string]any{"print_stats": map[string]string{"state": state}}
				}
				raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": response})
				_, _ = conn.Write(append(raw, 3))
			}()
		}
	}()
	client := testClient(t, func(_ http.ResponseWriter, r *http.Request) { t.Errorf("unexpected cloud request: %s", r.URL.Path) })
	client.Config.ClientMode = "printer"
	agent := &moonrakerAgent{prototype: client, load: func(context.Context) (Config, error) { return client.Config, nil }}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	message := moonrakerMessage{Method: "klippee_pull"}
	if _, err := agent.receive(ctx, message); err == nil || !strings.Contains(err.Error(), "printing") {
		t.Fatalf("mutating request during printing yielded %v", err)
	}
	idle.Store(true)
	response, err := agent.receive(ctx, message)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(response)
	var accepted moonraker.ExtensionResponse
	if err := json.Unmarshal(raw, &accepted); err != nil || !accepted.Accepted || accepted.Command != "apply" || accepted.OperationID == "" {
		t.Fatalf("invalid agent acknowledgement: %s (%v)", raw, err)
	}
	params, _ := json.Marshal(map[string]string{"operation_id": accepted.OperationID})
	for ctx.Err() == nil {
		response, err := agent.status(params)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(response)
		var status moonraker.ExtensionOperationStatus
		if err := json.Unmarshal(raw, &status); err != nil {
			t.Fatal(err)
		}
		if !status.Running {
			if !status.Found || status.OperationID != accepted.OperationID || status.LastResult == nil ||
				status.LastResult.Status != "failed" || !strings.Contains(status.LastResult.Summary, "sync baseline") || queries.Load() != 2 {
				t.Fatalf("legacy pull should reject a missing clean baseline: %s", raw)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("operation did not complete")
}

func TestNativeCaptureSafelyFollowsKlipperAddonLinks(t *testing.T) {
	root, addons := t.TempDir(), t.TempDir()
	writeFile(t, root, "printer.cfg", "[include addons/*.cfg]\n")
	writeFile(t, addons, "park.cfg", "[gcode_macro PARK]\n")
	writeFile(t, addons, "tokens.cfg", "private\n")
	if err := os.Symlink(filepath.Join(addons, "tokens.cfg"), filepath.Join(addons, "other.cfg")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(addons, filepath.Join(root, "addons")); err != nil {
		t.Fatal(err)
	}
	paths, err := configPaths(root)
	if err != nil || strings.Join(paths, ",") != "addons/park.cfg,printer.cfg" {
		t.Fatalf("unexpected capture graph %v: %v", paths, err)
	}
	if _, err := managedPath(root, "addons/park.cfg"); err == nil {
		t.Fatal("apply accepted a linked write target")
	}
}

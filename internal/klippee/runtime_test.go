package klippee

import (
	"archive/zip"
	"bytes"
	"compress/zlib"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeDiagnosticUploadPreservesBundleAndPrivateLinkContract(t *testing.T) {
	var client *Client
	client = testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/diagnostics") || r.Header.Get("Content-Type") != "application/zip" {
			t.Errorf("unexpected diagnostic upload %s %s", r.Method, r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		archive, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
		if err != nil {
			t.Error(err)
			return
		}
		entries := map[string]string{}
		for _, f := range archive.File {
			data, err := f.Open()
			if err != nil {
				t.Error(err)
				return
			}
			value, err := io.ReadAll(data)
			data.Close()
			if err != nil {
				t.Error(err)
				return
			}
			entries[f.Name] = string(value)
		}
		if entries["logs/klippy.log"] != "fixture Klippy log\n" ||
			!strings.Contains(entries["manifest.json"], `"schemaVersion":2`) ||
			!strings.Contains(entries["manifest.json"], `"archiveName":"snapshots/usb-serial-topology.json"`) {
			t.Errorf("incorrect diagnostic entries: %v", entries)
		}
		json.NewEncoder(w).Encode(map[string]string{"url": client.Config.BaseURL + "/log-diagnostics?upload=fixture"})
	})
	client.Config.ClientMode = "printer"
	t.Setenv("KLIPPEE_MOONRAKER_SOCKET", filepath.Join(t.TempDir(), "unavailable"))
	writeFile(t, client.Config.ConfigDir, "klippy.log", "fixture Klippy log\n")
	err := client.UploadDiagnostics(context.Background(), filepath.Join(client.Config.ConfigDir, "klippy.log"), false)
	if err != nil {
		t.Fatal(err)
	}
	if out := client.Out.(*bytes.Buffer).String(); !strings.Contains(out, "DIAGNOSTIC UPLOAD SUCCESSFUL") ||
		!strings.Contains(out, "/log-diagnostics?upload=fixture") {
		t.Fatalf("missing private diagnostic link: %s", out)
	}
}

func TestRuntimeDiagnosticDoesNotExposeUnknownSavedVariables(t *testing.T) {
	raw := json.RawMessage(`{"status":{"save_variables":{"variables":{"enable_box":true,"slot2":12,"slot1":"secret","retry_step":"QDE_001_002_03","secret_name":"private payload"}}}}`)
	filtered, err := safeDiagnosticVariables(raw)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(filtered)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(data)
	if strings.Contains(doc, "secret") || strings.Contains(doc, "private") ||
		!strings.Contains(doc, `"slot2":12`) || !strings.Contains(doc, `"omitted_variable_count":2`) {
		t.Fatalf("unexpected filtered diagnostic: %s", doc)
	}
}

type fakeBoxWire struct {
	write   func([]byte) error
	seq     byte
	payload []byte
}

func (w *fakeBoxWire) WritePacket(_ context.Context, packet []byte) error {
	return w.write(packet)
}

func (w *fakeBoxWire) ReadPacket(_ context.Context) (byte, []byte, error) {
	return w.seq, w.payload, nil
}

func TestBoxQueryExchangesOnlyFixedCommandsAfterDictionaryValidation(t *testing.T) {
	raw := []byte(`{"fixture":"box dictionary"}`)
	var compressed bytes.Buffer
	z := zlib.NewWriter(&compressed)
	z.Write(raw)
	z.Close()
	validated, revalidated := false, false
	var commands []byte
	wire := &fakeBoxWire{}
	wire.write = func(packet []byte) error {
		if int(packet[0]) != len(packet) || packet[len(packet)-1] != 0x7e {
			return fmt.Errorf("invalid outgoing frame: %x", packet)
		}
		wire.seq = (packet[1] + 1) & 0xf
		payload := packet[2 : len(packet)-3]
		commands = append(commands, payload[0])
		switch payload[0] {
		case 1:
			offset, tail, err := readBoxVLQ(payload[1:])
			if err != nil || len(tail) != 1 || int(offset) > compressed.Len() {
				return fmt.Errorf("invalid outgoing identify payload")
			}
			chunk := compressed.Bytes()[offset:]
			chunk = chunk[:min(len(chunk), int(tail[0]))]
			wire.payload = append(append(boxVLQ([]byte{0}, offset), byte(len(chunk))), chunk...)
		case 12:
			if !validated || !revalidated || !bytes.Equal(payload, []byte{0x0c, 0x02, 0x84, 0x80, 0x88, 0xf0, 0x74}) {
				return fmt.Errorf("unexpected RCC_CSR command: %x", payload)
			}
			wire.payload = boxVLQ([]byte{0x62}, 0x12000000)
		case 89:
			if !bytes.Equal(payload, []byte{0x59, 0x81, 0x25}) {
				return fmt.Errorf("unexpected reset-reason OID: %x", payload)
			}
			wire.payload = append([]byte{0x7e, 0x81, 0x25, 8}, []byte("Software")...)
		default:
			return fmt.Errorf("unexpected command: %x", payload)
		}
		return nil
	}
	s := boxSession{wire: wire}
	flags, reason, err := s.readReset(context.Background(), func(data []byte) error {
		validated = bytes.Equal(data, raw)
		return nil
	}, func() error {
		revalidated = true
		return nil
	})
	if err != nil || flags != 0x12000000 || reason != "Software" || !bytes.HasSuffix(commands, []byte{12, 89}) {
		t.Fatalf("unexpected reset query result: %x %s %v (commands %v)", flags, reason, err, commands)
	}
}

func TestBoxFramingUsesKlipperChecksum(t *testing.T) {
	// CRC-16/MCRF4XX check vector: reflected polynomial 0x1021, 0xffff
	// initialization, and no output XOR.
	if got := boxCRC([]byte("123456789")); got != 0x6f91 {
		t.Fatalf("CRC = %04x", got)
	}
}

func TestServiceActivationFailureRestoresOldFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "current.service", "[Unit]\n"+agentDescription+"\n")
	a, err := inspectServiceFile(filepath.Join(root, "current.service"), agentDescription)
	if err != nil {
		t.Fatal(err)
	}
	b, err := inspectServiceFile(filepath.Join(root, "new.timer"), timerDescription)
	if err != nil {
		t.Fatal(err)
	}
	reloaded := false
	err = installServiceFiles([]serviceEdit{{a, []byte("replacement")}, {b, []byte("new timer")}},
		func() error { return fmt.Errorf("failed to activate") },
		func() error { reloaded = true; return nil })
	data, readErr := os.ReadFile(a.path)
	_, statErr := os.Stat(b.path)
	if err == nil || !reloaded || readErr != nil || !bytes.Equal(data, a.before) || !os.IsNotExist(statErr) {
		t.Fatalf("activation rollback did not restore both files: %v / %v / %v, reloaded=%v", err, readErr, statErr, reloaded)
	}
}

func TestLegacyConnectionIsReadAsData(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config")
	secret := "a'$(echo_unsafe)"
	text := fmt.Sprintf("KLIPPEE_BASE_URL=https://klippee.xyz\nKLIPPEE_CONFIG_DIR='%s'\nKLIPPEE_REPOSITORY_ID=repo\nKLIPPEE_TOKEN='a'\"'\"'$(echo_unsafe)'\n", root)
	writeFile(t, root, "config", text)
	config, err := Load(context.Background(), path, "")
	if err != nil || config.ClientMode != "printer" || config.Token != secret || config.ConfigDir != root {
		t.Fatalf("legacy printer connection did not load: %v", err)
	}
}

func TestReadOnlyVariableCommandLeavesLegacyStateIntact(t *testing.T) {
	client := testClient(t, func(http.ResponseWriter, *http.Request) {})
	writeFile(t, client.Config.ConfigDir, ".klippee/variables.env", "nozzle=0.4\n")
	if err := client.Variables("doctor"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(client.state("variables.env")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(client.Config.ConfigDir, variablesName)); !os.IsNotExist(err) {
		t.Fatalf("read-only command created new variables file: %v", err)
	}
}

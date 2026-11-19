package moonraker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cl-tim/hlab/internal/printerlab/inventory"
)

func TestComponentInventoryOnlyReturnsHardwareEvidence(t *testing.T) {
	t.Setenv("PLAB_COMPONENT_TEST_KEY", "test-key")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Query().Get("configfile") != "config" || r.Header.Get("X-Api-Key") != "test-key" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		fmt.Fprint(w, `{"result":{"status":{"configfile":{"config":{
			"mcu EBBCan":{"canbus_uuid":"abc123","password":"excluded"},
			"extruder":{"sensor_type":"PT1000","gear_ratio":"50:10","nozzle_diameter":"0.4"},
			"extruder1":{"heater_pin":"PA0"},
			"beacon":{"serial":"/dev/serial/by-id/usb-Beacon_RevH"},
			"beacon model default":{"model_coef":"calibration-only"},
			"scanner model default":{"model_coef":"calibration-only"},
			"tmc2209 stepper_x":{"run_current":"0.8"},
			"heater_fan hotend":{"pin":"PA1"},
			"adxl345":{"cs_pin":"EBBCan:PB12"},
			"neopixel toolhead":{"pin":"PA2"},
			"gcode_macro HOTEND_MODEL":{"gcode":"Rapido SECRET"},
			"save_variables":{"filename":"/private/path"}
		}}}}}`)
	}))
	defer server.Close()
	client, err := New(inventory.Printer{MoonrakerURL: server.URL, APIKeyEnv: "PLAB_COMPONENT_TEST_KEY"}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := client.ComponentInventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Components) != 8 || snapshot.Components["extruder"].Settings["sensor_type"] != "PT1000" || snapshot.Components["mcu EBBCan"].Type != "controller" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if _, err := time.Parse(time.RFC3339Nano, snapshot.CapturedAt); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(snapshot)
	for _, forbidden := range []string{"password", "excluded", "Rapido", "SECRET", "/private/path", "model default", "calibration-only"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("snapshot leaked %q", forbidden)
		}
	}
}

func TestComponentInventoryRejectsMissingConfiguration(t *testing.T) {
	for _, response := range []string{`{}`, `{"result":{"status":{"configfile":{"config":{}}}}}`, `{"result":{"status":{"configfile":{"settings":{"mcu":{}}}}}}`, `{"error":{"message":"Klippy disconnected"}}`, `invalid JSON`} {
		t.Run(response, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, response) }))
			defer server.Close()
			client, err := New(inventory.Printer{MoonrakerURL: server.URL}, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.ComponentInventory(context.Background()); err == nil {
				t.Fatal("accepted missing configuration")
			}
		})
	}
}

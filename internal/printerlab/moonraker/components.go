package moonraker

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cl-tim/homelab-cli/internal/printerlab/inventory"
)

// ComponentInventory reads the active raw Klipper configuration. Only hardware
// sections and selected identifying settings leave this method; macros and
// unrelated configuration are neither persisted nor returned.
func (c *Client) ComponentInventory(ctx context.Context) (inventory.ComponentSnapshot, error) {
	var envelope struct {
		Result struct {
			Status struct {
				ConfigFile struct {
					Config map[string]map[string]string `json:"config"`
				} `json:"configfile"`
			} `json:"status"`
		} `json:"result"`
	}
	if err := c.get(ctx, "/printer/objects/query?configfile=config", &envelope); err != nil {
		return inventory.ComponentSnapshot{}, err
	}
	sections := envelope.Result.Status.ConfigFile.Config
	if len(sections) == 0 {
		return inventory.ComponentSnapshot{}, fmt.Errorf("Moonraker returned no active configfile.config; previous inventory retained")
	}
	snapshot := inventory.ComponentSnapshot{
		CapturedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Source:     "moonraker:configfile",
		Components: make(map[string]inventory.ConfiguredComponent),
	}
	for section, settings := range sections {
		kind := hardwareSectionType(section)
		if kind == "" {
			continue
		}
		component := inventory.ConfiguredComponent{Type: kind, Settings: make(map[string]string)}
		for key, value := range settings {
			if componentSetting(key) {
				component.Settings[key] = value
			}
		}
		snapshot.Components[section] = component
	}
	return snapshot, nil
}

func hardwareSectionType(section string) string {
	kind, _, _ := strings.Cut(section, " ")
	// These devices have one hardware section. Named calibration/model
	// sections (for example "beacon model default") are not additional probes.
	switch kind {
	case "beacon", "cartographer", "scanner", "probe", "bltouch", "load_cell_probe", "smart_effector":
		if section != kind {
			return ""
		}
	}
	switch kind {
	case "mcu":
		return "controller"
	case "beacon", "cartographer", "scanner", "probe", "probe_eddy_current", "bltouch", "load_cell_probe", "smart_effector":
		return "probe"
	case "fan", "heater_fan", "controller_fan", "fan_generic", "temperature_fan":
		return "fan"
	case "heater_bed", "heater_generic":
		return "heater"
	case "temperature_sensor":
		return "temperature-sensor"
	case "filament_switch_sensor", "filament_motion_sensor", "hall_filament_width_sensor", "tsl1401cl_filament_width_sensor":
		return "filament-sensor"
	case "adxl345", "lis2dw", "lis3dh", "mpu9250", "icm20948", "bmi160", "bmi270":
		return "accelerometer"
	case "tmc2130", "tmc2208", "tmc2209", "tmc2240", "tmc2660", "tmc5160":
		return "stepper-driver"
	case "extruder", "extruder_stepper":
		return "extruder"
	case "led", "neopixel", "dotstar", "pca9533", "pca9632":
		return "lighting"
	case "servo":
		return "servo"
	}
	if strings.HasPrefix(kind, "stepper_") {
		return "stepper"
	}
	// Klipper supports additional extruders named extruder1, extruder2, etc.
	if suffix, ok := strings.CutPrefix(kind, "extruder"); ok && suffix != "" && strings.Trim(suffix, "0123456789") == "" {
		return "extruder"
	}
	return ""
}

func componentSetting(key string) bool {
	switch key {
	case "serial", "canbus_uuid", "canbus_interface", "sensor_type", "sensor_pin",
		"heater_pin", "pin", "control_pin", "switch_pin", "endstop_pin", "analog_in_pin", "analog_out_pin",
		"spi_bus", "i2c_bus", "i2c_address", "i2c_mcu", "uart_address",
		"cs_pin", "uart_pin", "tx_pin", "diag_pin", "step_pin", "dir_pin", "enable_pin",
		"spi_software_sclk_pin", "spi_software_mosi_pin", "spi_software_miso_pin",
		"microsteps", "full_steps_per_rotation", "rotation_distance", "gear_ratio", "nozzle_diameter", "filament_diameter",
		"max_temp", "min_temp", "heater", "extruder", "axes_map", "run_current", "sense_resistor":
		return true
	}
	return false
}

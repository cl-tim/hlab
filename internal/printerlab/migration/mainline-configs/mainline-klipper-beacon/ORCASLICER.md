# OrcaSlicer recommendations

Calibrate input shaping and verify motion limits on each printer before
choosing speeds and accelerations in OrcaSlicer. Tune temperature, flow ratio,
pressure advance and maximum volumetric flow for each filament.

## Printer G-code

Use the standard Klipper/Moonraker connection and the following start G-code:

```gcode
PRINT_START BED=[bed_temperature_initial_layer_single] HOTEND={first_layer_temperature[initial_extruder]} CHAMBER=[overall_chamber_temperature] MESH=adaptive
SET_PRINT_STATS_INFO TOTAL_LAYER=[total_layer_count]
M83
```

Use only this end G-code:

```gcode
PRINT_END
```

Enable **Label objects** in the OrcaSlicer printer profile. Moonraker uses those
object definitions for Klipper's adaptive mesh, Smart Park and the adaptive
Voron purge. If no object definitions are present, Smart Park and Voron Purge
fall back to a fixed safe location; the Beacon mesh still scans continuously.

`MESH=adaptive` performs a fresh continuous Beacon scan for each print. Use
`MESH=default` only when you intentionally want to load a previously saved
`default` mesh, or `MESH=none` for diagnostics.

The current profile defaults to `SOAK=0` and a 5 mm adaptive-mesh margin.
Override them only when a material or print warrants it, for example:

```gcode
PRINT_START BED=110 HOTEND=260 CHAMBER=55 MESH=adaptive SOAK=120 MESH_MARGIN=8
```

Calibrate firmware retraction for the target printer and filament before enabling it in OrcaSlicer.

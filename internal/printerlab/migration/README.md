# Bundled Plus4 migration assets

Printerlab's workstation migration backend lives in `internal/printerlab/migrate` and is
compiled into `hlab` as Go. The executable also embeds the firmware fragments,
remote helpers and reviewed profiles here. It needs no adjacent files, QIDI
repository checkout or local Python interpreter to run migration commands.
SSH and the upstream MCU build toolchain remain prerequisites. Klipper's
upstream build uses Python inside Linux/WSL, and the printer's Klipper runtime
also uses Python.

Follow [QIDI_MAINLINE_KLIPPER.md](../QIDI_MAINLINE_KLIPPER.md) for the operator
workflow. `hlab printer firmware advanced paths [PRINTER]` prints the actual tool, profile, output
and recovery-backup paths. Tools are extracted under `PLAB_HOME/tooling` into a
directory identified by their embedded content hash. Builds, renders, downloads
and recovery backups use `PLAB_HOME/artifacts` and `PLAB_HOME/backups`, outside
the tool cache. `PLAB_HOME` defaults to the usual Printerlab config directory.

- `tools/migration/`: guarded printer helpers, Linux/WSL compilation helper,
  RK3328 tuning, smoke and commissioning configurations. The remaining
  `upgrade-klippee-agent.py` runs on the printer over SSH.
- `firmware-configs/`: reviewed MCU Kconfig fragments.
- `mainline-configs/mainline-klipper-beacon/`: full reviewed Plus4/Beacon profile.
- `mainline-configs/plus4-x7-v1.0-beacon/`: retained compatibility profile.
- `plr-hardening/`: Python modules and installer for stock-QIDI checkpoint
  maintenance. They run inside the printer's existing Klipper environment.

Optional `defaults.firmwareRepo` and `migration.profileDir` overrides select
custom assets and profiles. They do not select a backend implementation.
Remove the obsolete `defaults.python` key from older inventories. A modified
cached asset is rejected; move the affected cache aside for a clean extraction.

From the repository root, run `go test ./...` and `go vet ./...`.

Optional tests of the printer-side Python assets require Python/pytest and
Linux or WSL for the POSIX cases:

```bash
cd internal/printerlab
python3 -m pip install -r migration/requirements-dev.txt
python3 -m pytest
python3 -m pytest migration/tests/test_plr_hardening.py
```

Tests use fixtures and mock transport; they do not require a printer. See
[SOURCES.md](SOURCES.md) for provenance and the retained MIT license notices.

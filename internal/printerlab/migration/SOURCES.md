# Migration source provenance

Printerlab maintains and ships these sources directly. These repositories are
attribution and review references, not installation dependencies.

- `tools/migration`, `firmware-configs`, the `plus4-x7-v1.0-beacon` compatibility
  profile, and `plr-hardening` were imported from
  [qidi-plus4-firmware](https://github.com/timo-quinn/qidi-plus4-firmware),
  baseline commit `180ff85729a7b53f04c4dcf409ac03f166208eba`, with the reviewed
  2026-10-04 local migration fixes included. See `LICENSE.firmware`.
- `mainline-configs/mainline-klipper-beacon` was imported from
  [qidi-plus4-configs](https://github.com/cl-tim/qidi-plus4-configs), commit
  `1242dde42c7b78c96491a2e921a18116d9bd8815`. See `LICENSE.configs`.
- Native Armbian decompression uses
  [github.com/ulikunitz/xz](https://github.com/ulikunitz/xz), version `v0.5.17`.
  Its BSD notice is included in `LICENSE.xz` and the executable's asset bundle.

Runtime output paths and the default profile are adapted for Printerlab.
Workstation orchestration was ported to `internal/migrate/native*.go` on
2026-10-04. The former local Python tools were removed. Their reviewed probe
decisions are retained in `internal/migrate/testdata/probe-reference.json` as
888 regression cases. Remote Python assets remain for Klipper integration;
they execute on the printer, not as a workstation migration backend.
Preserve source notices when changing these files. Review backend, firmware
fragments, runtime profiles and regression tests together when updating them.
The Go bundle ID hashes the embedded paths and contents, so each executable
extracts its own version of the tools and checks it before use.

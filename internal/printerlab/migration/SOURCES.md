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

The optional runout-delay trimming and persistent USB evidence added in
November 2026 were independently implemented after reviewing
[Plus4-Grease](https://github.com/JarvisAKF/Plus4-Grease/tree/abc21a33b73758d34573d5f80de6b436d3241564).
No Grease implementation was copied. The USB sender/writer, transaction
integration and tests are maintained here; they do not download or invoke that
project. The service follows systemd's [socket activation protocol](https://www.freedesktop.org/software/systemd/man/sd_listen_fds.html)
and [readiness notification](https://www.freedesktop.org/software/systemd/man/sd_notify.html).

The later 300 mm/s tilt/mesh setting and `box_drying.py` were added after the
same review. The drying controller is an independent implementation with
per-Box monotonic deadlines and repeated filament-limit checks. It reads the
installed QIDI `officiall_filas_list.cfg` rather than shipping a copied limit
table. Offline parsing was checked against extracted 1.8.2 and 2.1.1 tables;
unused rows with blank limits remain unsupported. The bundled mainline
profile's two speed values were updated alongside the configuration repository;
its original import commit above remains the baseline provenance.

`qidi-fixes/box_output_clock_guard.py` and `box_status_compat.py` are copied
without code changes from `qidi-plus4-firmware` commit
`57a95ba0b7149018487984af83beb08f325891ac`, under its `qidibox-modules/` GPLv3
license, included at `qidi-fixes/LICENSE`. They run as separate printer-side
Klipper extensions; no compiled QIDI binaries are shipped. The maintenance
orchestrator is maintained by hlab and reuses the existing manifest-bound PLR
bundle. Log-limit settings follow that commit's
`docs/fixes/INSTALL-LOG-AND-BOX-FIXES.md`. Firmware scope and the decision not
to add the legacy status shim on 2.1.1 follow its installed-printer review.

Workstation orchestration was ported to `internal/migrate/native*.go` on
2026-10-04. The former local Python tools were removed. Their reviewed probe
decisions are retained in `internal/migrate/testdata/probe-reference.json` as
888 regression cases. Remote Python assets remain for Klipper integration;
they execute on the printer, not as a workstation migration backend.
Preserve source notices when changing these files. Review backend, firmware
fragments, runtime profiles and regression tests together when updating them.
The Go bundle ID hashes the embedded paths and contents, so each executable
extracts its own version of the tools and checks it before use.

## Stock QIDI release catalogue

`tools/migration/qidi-releases.json` pins the Plus4 2.1.1 SoC and touchscreen
payloads from QIDI's Aliyun OTA service. The payloads were inspected on
2026-11-11; host embedded versions are SOC V2.1.1, UI V211, MCU V0.12.0.
The catalogue records the exact URLs, byte lengths and SHA-256 hashes; vendor
binaries are downloaded at runtime and are not embedded or redistributed.

References:

- [Official update procedure](https://wiki.qidi3d.com/en/X-Plus4/Manual/firmware-update)
- [Package/installer analysis](https://github.com/timo-quinn/qidi-plus4-firmware/blob/main/docs/firmware/FIRMWARE-2.1.1.md)
- [Upload protocol](https://itead.cc/nextion/nextion-hmi-upload-protocol/comment-page-1/)

## Durable stock saved-variable writes

`qidi-fixes/save_variables_fast.py` is maintained here under GPLv3, inspired by
[Valve-Oil's saved-variable optimization](https://github.com/thelegendtubaguy/Valve-Oil/blob/c89dddc475b73e060027ff5a2489239df1b14406/installer/klipper/extras/save_variables_fast.py).
It adds file and directory fsync, uses QIDI's in-memory ConfigParser/literal
round trip, exposes hook/write counters, and omits Valve-Oil's variable-name
migration. Installation is restricted to the extracted stock Plus4 1.8.2 and
2.1.1 source fingerprints, including each version's Box binary. No vendor
binary is shipped. The GPLv3 stock `save_variables.py` in the test fixtures is
from [QIDI's Klipper source](https://github.com/QIDITECH/klipper/blob/4bb7c6337936ef273e621d1f55bc0ef92114785d/klippy/extras/save_variables.py),
SHA-256 `0327ce54b1a7286d97b0b2fbe008ecc9a36057a5d12642fd6237552c5e77b682`.
Tests compare actual saved files and status against this stock implementation.

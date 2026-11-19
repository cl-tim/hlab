# Migration probe reference cases

`probe-reference.json` contains stock and migrated fixture observations and
888 mutations evaluated by the reviewed Python backend before its Go port on
2026-10-04. The original backend came from the firmware source identified in
`migration/SOURCES.md`.

Each case records the expected statuses in `nativeProbeDefinitions` order.
Missing, null, false, zero and malformed observations are distinct cases.
`TestNativeProbesMatchReviewedReference` applies each mutation and checks the
Go evaluator without an interpreter or a printer.

Other native backend tests cover rendering ownership, MCU vectors and build
manifests, streamed backups, download integrity, the pre-erase recovery dump,
factory imaging, PLR stop/apply/verify transactions and start stability checks.

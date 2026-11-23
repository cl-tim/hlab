# Build and contribute to the documentation

The Markdown in `docs/` is the authoritative source for both the published
site and the documentation in this repository. The root `README.md` is a short
entry point.

## Build locally

Use Python 3.9 or newer. From the repository root, install the pinned docs
dependencies and build strictly:

```sh
python -m pip install -r requirements-docs.txt
python -m mkdocs build --strict
```

The site is written to `site/`, which is ignored by Git. `mkdocs.yml` limits
the source directory to `docs/`, so repository files, local configuration,
credentials and build outputs are not copied into the site. The Pages workflow
builds into the runner's temporary directory and uploads only that generated
site directory.

Before submitting changes, add each new page to the navigation in `mkdocs.yml`
and check its relative links and heading fragments. MkDocs validates navigation,
targets and anchors during the strict build. Keep printer instructions
within the existing user and equipment safety boundaries. Do not add real
printer or runtime captures to the site.

## Develop hlab

Run the code checks and make a release build from the repository root:

```bash
make check      # format, vet, and test; use .\build.ps1 check on Windows
make release    # cross-compile into dist/
```

SSH code paths are tested against an in-process SSH server
([internal/sshtest](https://github.com/cl-tim/hlab/blob/main/internal/sshtest/server.go)),
so no real machine is needed. Enrollment tests run against a container and
skip unless `HLAB_TEST_SSH_ADDR` is set; see the
[enrollment test fixture](https://github.com/cl-tim/hlab/tree/main/internal/enroll/testdata/enroll-target).
[CLAUDE.md](https://github.com/cl-tim/hlab/blob/main/CLAUDE.md) documents the
architecture and the reasoning behind it.

## Site navigation

- [Getting started](getting-started.md)
- [Linux fleet management](linux/index.md)
- [QIDI Plus4 guide](guides/qidi-plus4.md)
- [Klippee guide](guides/klippee.md)
- [Printer command reference](reference/printers.md)
- [Firmware migration](reference/firmware.md)
- [Stock QIDI fixes](reference/stock-fixes.md)
- [Stock QIDI firmware updates and recovery](reference/stock-updates.md)
- [Research and operations notes](research/plus4-box-heater-refresh.md)

Klippee's own documentation is also published at
<https://cl-tim.github.io/klippee-web/>.

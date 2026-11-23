# Backups and recovery

`hlab backup [SELECTOR]` archives hlab-managed stack state from selected
machines to the computer where you run hlab. The archive includes the Compose
files and data under `/opt/hlab/stacks`; it excludes bulk data mounted from
elsewhere, logs, caches and database files. It is a focused copy of
hlab-managed service configuration and data, not a full-machine or database
backup. A host with no hlab-managed stacks is reported as having nothing to
back up.

By default, backups are written under `$HLAB_HOME/backups/<host>/` with a
timestamped `.tar.gz` archive and adjacent `.sha256` file. The remote host
checksums its archive and hlab verifies the transferred byte count, checksum
and readability. Backups are created with private file permissions because
they may contain service credentials and printer configuration.

```bash
hlab backup @servers
hlab backup apps-1 --out "$HOME/secure-backups"
hlab backup verify @servers --max-age 168h
```

`hlab backup verify [SELECTOR]` checks the latest local archive for each
selected host against its saved checksum. `--max-age` makes an archive older
than the specified duration a failure; the default `0` disables the age
limit. `--dir` selects another directory containing per-host backup folders.

## Back up hlab's own inventory

Add `--self` to include hlab's inventory in the same backup output. The
management key is excluded by default. It is equivalent to root access on all
enrolled machines; include it only with the deliberate
`--include-key` decision and keep that archive accordingly protected.

```bash
hlab backup --self --out "$HOME/secure-backups"
```

## Restore or rehearse recovery

`hlab restore HOST` previews restoring the newest archive taken from that same
host. Use `--from ARCHIVE` to choose another archive, including one from a
machine being replaced. `--apply` is required to change the host. Before
restoring, hlab verifies the local checksum and archive; the remote restore
verifies it again, stops stacks, keeps the current state aside under
`/var/lib/hlab`, unpacks, then starts the stacks again. This can overwrite
live service data, so confirm the target and archive before applying.

```bash
hlab restore apps-1
hlab restore apps-1 --from "$HOME/secure-backups/apps-1/20261123-150000.tar.gz" --apply
```

`hlab backup drill HOST --from ARCHIVE` rehearses restoration to a disposable
recovery machine, then checks its health and configured endpoints. The target
must be a managed host tagged `recovery-drill` and have at least one endpoint
configured. The command previews by default; `--apply` restores the archive
and starts services on that recovery host.

```bash
hlab backup drill recovery-1 --from "$HOME/secure-backups/apps-1/20261123-150000.tar.gz"
hlab backup drill recovery-1 --from "$HOME/secure-backups/apps-1/20261123-150000.tar.gz" --apply
```

Use a disposable host with data you can replace: the applied drill restores
the selected archive onto it. A passing drill checks the restored host using
the ordinary health checks; see [facts and health](facts-health.md).

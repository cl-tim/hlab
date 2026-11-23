# Remote commands, output and logs

## Run a command

`hlab run SELECTOR -- COMMAND...` sends a command to every selected managed
host in parallel. Words after `--` are joined and passed to the remote login
shell, so pipes, redirects and `&&` work. Quote compound commands for your
local shell. This command executes what you provide; it does not have a
preview mode. Use `--confirm` to display the target list and ask before
running.

```bash
hlab run @servers -- uptime
hlab run '#debian' --confirm -- 'apt-get update && apt-get -y upgrade'
hlab -o json run @servers -- 'free -m'
```

`run` exits nonzero if a host cannot be reached or the remote command fails.
The global `-c/--concurrency` setting limits how many hosts run at once; the
default is 10 unless configured otherwise. Table output is the default;
`-o json` and `-o yaml` return structured results for scripts.

## Fetch logs

`hlab logs SELECTOR NAME` fetches recent logs for a container or systemd unit
named `NAME` on the selected hosts. It defaults to 50 lines per host. Use
`--lines`, `--since`, and `--grep` to narrow the output; `--grep` is a
case-insensitive substring filter.

```bash
hlab logs @servers caddy --lines 100
hlab logs apps-1 homepage --since 1h --grep error
```

`--since` is passed through to Docker or systemd journal tooling; choose a
format accepted by the backend that provides those logs. See the command's
`--help` for current details. For fleet-wide status, see
[facts and health checks](facts-health.md).

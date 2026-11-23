# Stack secrets and credentials

Store runtime credentials beside the stack that uses them, not inside the
Compose file. Hlab writes them under `/opt/hlab/stacks/<project>/` on the
target host with mode `0600`. Compose files are easier to review and share
when secrets are referenced as files or environment files rather than
embedded as values.

## Create or store a secret

`hlab secret gen HOST PROJECT FILE` creates a random value on the machine
that will use it. The value does not cross the network. By default the whole
file contains the secret, for an application's `_FILE` convention. Use
`--env KEY` to write a `KEY=value` line into an environment file; existing
keys in that file are retained. `--force` replaces an existing secret.

```bash
hlab secret gen apps-1 web config/api-key
hlab secret gen apps-1 web app.env --env API_KEY
```

`hlab secret set HOST PROJECT FILE` prompts for a value without echo. It
requires an interactive terminal and sends the value as command input, not
as shell text or a command-line argument. Use `--env KEY` when storing a
single key in an environment file.

```bash
hlab secret set apps-1 web app.env --env THIRD_PARTY_TOKEN
```

## Inspect secrets carefully

`hlab secret list [SELECTOR]` reports secret filenames, modes and sizes but
never prints values. Modes other than `0600` or `0400` are flagged. Listing
uses elevated access where needed to read root-owned stack directories.

`hlab secret show HOST PROJECT FILE` prints one value to stdout. Use it only
when you deliberately need to see that value, and consider where terminal
output is recorded.

```bash
hlab secret list @servers
hlab secret show apps-1 web config/api-key
```

Backups include the stack data where these files live. Protect backup output
like the credentials themselves; see [backups and recovery](backups.md).

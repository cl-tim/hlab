# Docker Compose stacks

hlab keeps reviewed Compose source under `stacks/` in hlab's local home
directory, separate from the current working directory. On a managed machine,
hlab deploys it under `/opt/hlab/stacks/<project>/compose.yaml`.

## Deploy and inspect

`hlab stack deploy HOST PROJECT` deploys the saved project. With `--file`, you
can deploy a Compose file directly; its base name becomes the project name
unless you set `--name`. Use repeatable `--with FILE` options to ship companion
files such as a Caddyfile. A deployment is plan-only until `--apply`; add
`--sync` to collect host facts after a successful deploy.

```bash
hlab stack deploy apps-1 web
hlab stack deploy apps-1 web --apply --sync
hlab stack deploy apps-1 --file ./compose.yaml --with ./Caddyfile --apply
```

Before applying, hlab checks Compose-published ports against the last
collected listening-port facts. Run `hlab sync HOST` to refresh those facts.
Port conflicts are refused unless `--force` is supplied, for example when a
service being replaced still holds the port. Deployment runs
`docker compose up -d`; repeating an unchanged deployment is safe.

`hlab stack list HOST` reads the Compose projects Docker sees and identifies
which ones hlab owns. `hlab stack diff HOST [PROJECT]` compares local saved
Compose files with the deployed files and reports changes; it does not deploy
them.

## Bring an existing project under management

`hlab stack pull HOST [PROJECT]` copies hand-created Compose files to
`$HLAB_HOME/stacks/`. It does not change the host. Review the pulled files
before deploying or committing them because Compose files may contain
credentials. Pulling all projects skips ones already managed by hlab.

`hlab stack adopt HOST PROJECT` moves a hand-made project under
`/opt/hlab/stacks/` and applies the reviewed local Compose file. First pull
and inspect the project; adoption is plan-only unless `--apply` is given.
Relative bind-mounted data moves with the project directory. If the stack
does not come up, hlab restores its previous Compose file and location and
starts it again there.

```bash
hlab stack pull apps-1 web
hlab stack adopt apps-1 web
hlab stack adopt apps-1 web --apply
```

`hlab stack rename HOST OLD NEW` previews a rename of a managed project; add
`--apply` to perform it. If the stack does not start under its new name, hlab
puts it back. Compose derives network and volume names from the project name;
named volumes do not follow a rename, so the command refuses if the project
declares them unless `--force` is supplied. Move the named-volume data yourself
before forcing the rename, and review the Compose file and data paths first.

## Docker installation

`hlab stack install-docker HOST` installs Docker through Docker's convenience
script. It shows its plan first and requires `--apply` to run the installer.

Stack credentials have their own guidance in [secrets](secrets.md); protect
them from source control and include them in secured backups as appropriate.

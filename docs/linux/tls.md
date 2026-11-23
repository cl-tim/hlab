# Private CA and device HTTPS

`hlab tls` provisions a shared private CA and HTTPS listeners on enrolled Linux
machines and printers. Each listener runs on the device that owns the service.
SSH addresses and printer hostnames retain their meaning. The connection from
the browser to the device is encrypted; the final proxy hop stays on loopback.

Caddy generates the CA and server keys on the servers, issues certificates over
ACME, and renews them without this workstation. hlab retrieves only the public
root over authenticated SSH and pins its SHA-256 fingerprint. It refuses a
changed root instead of silently trusting a replacement.

## Configuration

Add `tls:` to the same configuration file as `hosts:` and `printerlab:`. For
example, with an enrolled `apps-1` host and `voron350` printer:

```yaml
tls:
  authority: apps-1
  authorityDNS: apps-1.lan
  authorityPort: 8443
  name: Home Network CA
  trustOnEnroll: true
  # Optional DNS configuration for the HTTPS daemon only. This is useful on
  # vendor printer images whose OS resolver cannot resolve internal names.
  resolvers: [192.168.1.4, 192.168.1.3]
  # Optional identities for appliance CSRs that an administrator will import.
  externalNames: [nas.lan, "192.168.1.1"]
  services:
    - name: homepage
      target: host:apps-1
      names: [homepage.lan]
      upstream: http://127.0.0.1:3000
    - name: printer
      target: printer:voron350
      names: [voron350.lan]
      upstream: http://127.0.0.1:80
    - name: plex
      target: host:media-1
      names: [media-1.lan]
      port: 32443
      upstream: http://127.0.0.1:32400
```

Targets reference existing inventory entries and use their SSH credentials.
Host targets must be managed. Printer targets use the printer inventory's
account, which needs passwordless sudo. If a printer's computer is also a managed
host, use its `host:` target when that account has the necessary privileges.
Use one inventory target per physical device; a device refuses a later apply
from a different target. For an intentional inventory rename, review and update
the ownership comment in its managed Caddyfile before applying the new target.

Names are explicit DNS SANs. Wildcards, conflicting device assignments, remote
upstreams, and proxy loops are rejected. The upstream must be an HTTP(S) origin
on `127.0.0.1`, `localhost`, or `::1`. HTTPS upstreams require a valid trusted
certificate too; certificate verification is never disabled.

The CA listens on 8443 by default, independently of existing web servers. Other
listeners default to 443. A service that already owns a port keeps it: select a
free HTTPS port or configure the existing server separately. A machine's current
HTTP server is preserved; hlab does not enable HTTP redirects or HSTS during
bootstrap.

## Provision

Commands preview their changes unless `--apply` is supplied. Review the first
run before applying:

```bash
# Publish CA and service names at the actual device addresses.
hlab tls dns apps-1
hlab tls dns apps-1 --apply

# Create the CA, pin its public root, and trust it on its own host.
hlab tls ca
hlab tls ca --apply

# Install public trust on enrolled machines and on this workstation.
hlab tls trust --apply
hlab tls trust --printers --apply
hlab tls trust --local --apply

# Install listeners, generate device keys, and obtain certificates.
hlab tls apply --apply
hlab tls apply --printers --apply
hlab tls status
```

Host and printer selectors are separate, as elsewhere in hlab:

```bash
hlab tls apply '@servers,!#retired' --apply
hlab tls apply voron350 --printers --apply
hlab tls trust qidi2 --printers --apply
```

`tls dns` updates only the declared CA and service names in the chosen
Technitium primary zone. Unrelated records are preserved. Secondary zones
receive changes through normal replication. Names that match an enrolled host
cannot be redirected away from that host. Printer addresses given as DNS names
must already resolve unambiguously to IPv4; use a stable inventory IP when
bootstrapping an unresolved name. External appliance names are not moved.

The machines need systemd, `ss`, OpenSSL, base64, and a supported CA trust
utility. hlab downloads the pinned Caddy release from its official GitHub
release, checks its SHA-256 or SHA-512 archive checksum, and transfers only the
binary over SSH. Linux amd64, arm64 and armv7 are supported. Existing vendor
Klipper, Moonraker, nginx and camera services remain separate.

The CA's ACME directory is:

```text
https://<authorityDNS>:<authorityPort>/acme/hlab/directory
```

Issuance is restricted to declared names and requires a TLS-ALPN-01 domain
control challenge on port 443. The authority must resolve and reach those
devices. Native certificates use Caddy's short-lived defaults (normally 12
hours). Keep the authority running for renewal. Devices powered off for longer
periods obtain a new certificate when they start again and can reach the CA.

After adding or removing services, run `tls dns`, `tls ca` and `tls apply`
again with `--apply`. Removed listeners are pruned when their device is next
applied with at least one declared service. Removing the final service from a
device requires explicitly stopping/disabling `hlab-tls.service` there; hlab
does not silently decommission a machine omitted from the configuration.

## Trust stores and enrollment

Linux uses `/usr/local/share/ca-certificates` with `update-ca-certificates`, or
`/etc/pki/ca-trust/source/anchors` with `update-ca-trust extract`. The anchor's
filename contains its fingerprint. Installation is repeatable and verified
against the system store with OpenSSL.

With `trustOnEnroll: true`, future **host** enrollments install the already
pinned CA after recording the host. CA bootstrap is never an enrollment side
effect. Missing CA state, unavailable sudo or failed trust installation makes
enrollment report a failure while retaining the successfully enrolled host.
Run `tls trust <selector> --apply` to repair that step. Printer inventory entry
creation and firmware migration do not install trust automatically; use
`tls trust --printers` after printer SSH setup.

`tls trust --local` installs into the current Windows user's Root store using
`certutil`, or the Linux system store using passwordless sudo. Other operating
systems and unmanaged clients can import the exported public certificate:

```bash
hlab tls export --out home-network-root.crt
```

Browsers or applications with independent trust stores need their own import.
Containers do not inherit host trust. For example, mount the public root into a
Node container and set `NODE_EXTRA_CA_CERTS` to its container path. Never mount
the CA's private key into an application container.

## Application settings

TLS certificates do not replace an application's origin and proxy checks:

- Mainsail's remote printer entries should use HTTPS port 443. Moonraker must
  allow the HTTPS Mainsail origin in `cors_domains`. Camera URLs also need HTTPS
  or paths relative to the secure printer origin.
- Home Assistant must trust the local proxy address. Use the installed
  version's HTTP settings interface; versions that have migrated HTTP settings
  to storage no longer apply later changes to YAML alone.
- Cockpit needs its HTTPS origin and `ProtocolHeader = X-Forwarded-Proto` when
  proxied. Restrict its backend socket to loopback before trusting that header.
- Update dashboard links, API endpoints, webcam streams and application-specific
  server URLs. An HTTPS page cannot load HTTP resources as active content.

`tls status` verifies the hostname and full chain against the pinned root,
reports expiry and HTTP status, and exits nonzero on connection or application
errors. Authentication responses 401 and 403 are accepted. It never skips
certificate verification. `tls apply` waits up to 30 seconds for the device's
declared certificates to verify; an active daemon alone is not success.

## Router and NAS certificates

Appliances can generate a private key and a CSR in their own certificate UI.
Keep that key there, include the DNS/IP SANs actually used by clients, and add
only those identities to `externalNames`. Sign the public CSR with:

```bash
hlab tls sign --csr nas.csr --out nas-chain.crt
hlab tls sign --csr nas.csr --out nas-chain.crt --days 90 --apply
```

Import the returned certificate and intermediate chain on the appliance, and
distribute the root with `tls export`. CSR signatures, key strength, SAN policy,
returned public key and certificate chain are checked. Requested CA powers and
other CSR extensions are not copied. Output files must not already exist.

Appliance imports use a separate 365-day intermediate signed by the same root;
its key stays on the authority. The intermediate rotates before it would expire
earlier than a newly requested certificate. Manual certificates are valid for
at most 90 days and require renewal/import before expiry. hlab does not log into
vendor appliance UIs or schedule their imports.

## State, backup and recovery

On each device:

| Path | Contents |
| --- | --- |
| `/etc/hlab/tls/` | Managed Caddyfile, public root and optional daemon resolver file |
| `/var/lib/hlab-tls/` | Private CA/server keys, certificates and ACME state; mode 0700 |
| `/usr/local/lib/hlab/caddy` | Checksum-verified Caddy binary |
| `/etc/systemd/system/hlab-tls.service` | Restricted service account and systemd unit |
| `/run/hlab-tls/admin.sock` | Private control socket; directory mode 0700 |

The controller holds the public pinned root under
`<HLAB_HOME>/pki/<authority-id>/root.crt` and downloaded archives under
`<HLAB_HOME>/cache/tls/`. No private CA or device key is fetched by provisioning.

Back up the authority's `/var/lib/hlab-tls` **in an encrypted backup**, together
with `/etc/hlab/tls` and its systemd unit. Stop `hlab-tls.service` while taking a
consistent snapshot of its ACME database, then start it again. Keep a copy away
from the authority's disk. The existing Compose stack backup covers
`/opt/hlab/stacks` and does **not** include this new state automatically.

Restore ownership, modes and the same CA state before restarting the service.
`tls ca --apply` refuses to create a replacement when the controller has a pin
but the server's CA is missing. Restore a lost root from backup. Replacing or
compromising a root requires an explicit rollout of new anchors and removal of
the old ones on every client; automatic root replacement is intentionally
unsupported. Certificate issuance does not provide revocation enforcement for
already trusted leaf certificates; protect the authority and its backups.

For diagnostics:

```bash
hlab run apps-1 -- 'sudo -n systemctl status hlab-tls.service'
hlab run apps-1 -- 'sudo -n journalctl -u hlab-tls.service --since "10 minutes ago"'
```

Configuration is syntax-checked before publication. A rejected reload restores
the previous file and leaves the running configuration serving. Startup,
certificate issuance and backend failures are reported and can be retried
after correcting DNS, port conflicts, proxy settings or offline devices.

References: [Caddy automatic HTTPS](https://caddyserver.com/docs/automatic-https),
[Caddy ACME server](https://caddyserver.com/docs/caddyfile/directives/acme_server),
[Cockpit proxy configuration](https://cockpit-project.org/external/wiki/Proxying-Cockpit-over-NGINX.html).

package stack

import "strings"

// Docker install markers.
const (
	MarkerDockerPresent   = "hlab:docker-present"
	MarkerDockerInstalled = "hlab:docker-installed"
)

// InstallDockerScript installs Docker using the vendor's convenience script.
//
// get.docker.com pipes a remote script into a root shell, which is a real
// supply-chain exposure — but it is Docker's own recommendation, it configures
// the official apt repository rather than a distribution snapshot, and the
// alternative of hand-rolling the repository and key is more code to get wrong.
// The script is downloaded and checked before it is run, rather than piped
// straight into sh, so a truncated or empty download cannot half-execute.
const InstallDockerScript = `
set -eu

if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
  echo '` + MarkerDockerPresent + `'
  docker --version
  exit 0
fi

if ! command -v curl >/dev/null 2>&1; then
  echo 'hlab:error curl is not installed' >&2
  exit 10
fi

TMP=$(mktemp)
trap 'rm -f "$TMP"' EXIT

curl -fsSL https://get.docker.com -o "$TMP"

# A truncated download would otherwise run as far as it got.
if [ ! -s "$TMP" ] || ! tail -n 1 "$TMP" >/dev/null 2>&1; then
  echo 'hlab:error the installer download was empty' >&2
  exit 11
fi
if ! grep -q 'docker' "$TMP"; then
  echo 'hlab:error the installer download does not look like the docker script' >&2
  exit 12
fi

sh "$TMP"

if ! command -v docker >/dev/null 2>&1; then
  echo 'hlab:error docker is still not present after installation' >&2
  exit 13
fi

systemctl enable --now docker >/dev/null 2>&1 || true

echo '` + MarkerDockerInstalled + `'
docker --version
`

// DockerVersionFrom pulls the reported version out of the install output.
func DockerVersionFrom(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "Docker version") {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

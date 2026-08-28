#!/usr/bin/env bash
# Install the Accorda agent on a Linux host (docs/ACCORDA.md §23).
#
# Downloads the static Linux binary from the GitHub release (latest by
# default, or a specific version), verifies its SHA-256 checksum, installs it
# to /usr/local/bin, and registers a systemd service that runs the
# reconciliation loop (`accorda sync --watch`) on boot and restarts on
# failure.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/accordahq/accorda/main/install.sh | sudo sh
#   curl -fsSL ... | sudo sh -s -- --version v0.1.0
#   curl -fsSL ... | sudo sh -s -- --no-service
#
# Flags:
#   --version <vX.Y.Z>   install a specific release instead of the latest
#   --no-service         install the binary only, without the systemd service
#   --service-name <n>   systemd unit name (default: accorda)
#   --project-dir <dir>  directory the service reconciles (default: /etc/accorda)
#
# Requires root (writes to /usr/local/bin and /etc/systemd/system) and curl.
set -euo pipefail

owner=accordahq
repo=accorda
version=""
no_service=0
service_name=accorda
project_dir=/etc/accorda

usage() {
  echo "usage: $0 [--version vX.Y.Z] [--no-service] [--service-name <n>] [--project-dir <dir>]" >&2
  exit 2
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version) version="$2"; shift 2 ;;
    --no-service) no_service=1; shift ;;
    --service-name) service_name="$2"; shift 2 ;;
    --project-dir) project_dir="$2"; shift 2 ;;
    -h|--help) usage ;;
    *) usage ;;
  esac
done

if [[ "$(id -u)" -ne 0 ]]; then
  echo "error: install.sh must run as root (sudo sh install.sh)" >&2
  exit 1
fi

if ! command -v curl >/dev/null 2>&1; then
  echo "error: curl is required" >&2
  exit 1
fi

# --- resolve version and architecture ------------------------------------

arch="$(uname -m)"
case "$arch" in
  x86_64|amd64) goarch=amd64 ;;
  aarch64|arm64) goarch=arm64 ;;
  *)
    echo "error: unsupported architecture: $arch" >&2
    exit 1
    ;;
esac

if [[ -z "$version" ]]; then
  version="$(curl -fsSL "https://api.github.com/repos/${owner}/${repo}/releases/latest" \
    | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1)"
  if [[ -z "$version" ]]; then
    echo "error: could not determine the latest release" >&2
    exit 1
  fi
fi

asset="accorda-linux-${goarch}"
base_url="https://github.com/${owner}/${repo}/releases/download/${version}"

echo "Installing Accorda ${version} (${goarch})"

# --- download and verify --------------------------------------------------

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

curl -fsSL -o "$tmpdir/$asset" "$base_url/$asset"
curl -fsSL -o "$tmpdir/checksums.txt" "$base_url/checksums.txt"

expected="$(awk -v name="$asset" '$2 == name { print $1 }' "$tmpdir/checksums.txt")"
if [[ -z "$expected" ]]; then
  echo "error: no checksum found for $asset in the release" >&2
  exit 1
fi
actual="$(sha256sum "$tmpdir/$asset" | awk '{ print $1 }')"
if [[ "$actual" != "$expected" ]]; then
  echo "error: checksum mismatch for $asset" >&2
  echo "  expected: $expected" >&2
  echo "  actual:   $actual" >&2
  exit 1
fi

install -m 0755 "$tmpdir/$asset" "/usr/local/bin/accorda"
echo "Installed /usr/local/bin/accorda"

if [[ "$no_service" -eq 1 ]]; then
  echo "Skipping systemd service (--no-service)"
  exit 0
fi

# --- systemd service ------------------------------------------------------

if ! command -v systemctl >/dev/null 2>&1; then
  echo "warning: systemctl not found; binary installed but no service registered" >&2
  exit 0
fi

mkdir -p "$project_dir"
unit="/etc/systemd/system/${service_name}.service"
cat > "$unit" <<EOF
[Unit]
Description=Accorda GitOps reconciliation agent
After=docker.service network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/accorda sync --watch --dir ${project_dir}
Restart=on-failure
RestartSec=5
# Hardening
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable "$service_name"
echo "Registered and enabled systemd service ${service_name}.service"
echo "Start it with: systemctl start ${service_name}"
echo "Place an accorda.yaml project file in ${project_dir} before starting."

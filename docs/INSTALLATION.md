# Installation

> **Status: available.** Official release artifacts are published as GitHub
> releases (static Linux binaries for `amd64` and `arm64`), and the install
> script downloads, verifies, and registers the agent as a systemd service.

The [usage guide](USAGE.md) assumes that an `accorda` executable is installed
and available on `PATH`:

```bash
accorda version
```

The current Docker Compose target also requires:

- a reachable Docker Engine;
- Docker Compose v2, available as `docker compose`;
- network access to the configured Git repository and container registries;
- Git credentials suitable for the repository, such as an SSH private key or
  HTTPS token.

The system `git` executable is not required at runtime. Accorda accesses Git
repositories through its built-in Git adapter.

## Install the latest release

On a Linux host, download and run the install script as root. It installs the
latest release binary to `/usr/local/bin/accorda` and registers a systemd
service that runs the reconciliation loop (`accorda sync --watch`) on boot and
restarts on failure:

```bash
curl -fsSL https://raw.githubusercontent.com/accordahq/accorda/main/install.sh | sudo sh
```

The service expects an `accorda.yaml` project file in `/etc/accorda` (override
with `--project-dir`). Start it after placing the project file:

```bash
sudo systemctl start accorda
```

## Install a specific version

Pass `--version` to install a particular release instead of the latest:

```bash
curl -fsSL https://raw.githubusercontent.com/accordahq/accorda/main/install.sh | sudo sh -s -- --version v0.1.0
```

## Install the binary only

Use `--no-service` to install the binary without registering a systemd
service:

```bash
curl -fsSL https://raw.githubusercontent.com/accordahq/accorda/main/install.sh | sudo sh -s -- --no-service
```

## Manual download

Releases are published on the [releases page](https://github.com/accordahq/accorda/releases).
Each release ships `accorda-linux-amd64`, `accorda-linux-arm64`, and a
`checksums.txt` for verification. The binary is a self-contained static Linux
executable; download, verify, and place it on `PATH`:

```bash
curl -fsSL -o accorda https://github.com/accordahq/accorda/releases/latest/download/accorda-linux-amd64
curl -fsSL -o checksums.txt https://github.com/accordahq/accorda/releases/latest/download/checksums.txt
sha256sum -c <(grep accorda-linux-amd64 checksums.txt)
install -m 0755 accorda /usr/local/bin/accorda
```

## Development builds

Contributors can use the development build instructions in the [repository
README](../README.md#quick-start).

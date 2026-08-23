# Installation

> **Status: TBD.** Official release artifacts, package repositories, container
> images, signature verification, and service-manager installation instructions
> have not been published yet.

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

Until supported installation methods are documented here, contributors can use
the development build instructions in the [repository README](../README.md#quick-start).

# Ember

Ember is a private, local-first, single-node Azure-like platform. The command
line binary stores bounded state below the directory passed with
`--state-dir` (or `.ember-state` by default).

## Requirements

- Linux or another Go-supported clean machine
- Go 1.22 or newer for a source build
- `sha256sum` and `install` for packaging and installation

No network service, cloud account, or external runtime is required.

## Build, package, and install

```text
make test
make package
PREFIX="$HOME/.local" make install
```

`make package` writes a platform-labelled binary and SHA-256 sidecar below
`dist/`. `make install` verifies the sidecar when present and installs the
binary as `$PREFIX/bin/ember`; it does not modify the source tree or a live
service. Set `GOOS`, `GOARCH`, `OUT_DIR`, `PREFIX`, or `BINARY` to override
these defaults.

## Golden clean-machine lifecycle

Run the bounded smoke path after packaging:

```text
make smoke
```

The smoke path creates and reads a group, creates a bucket, writes and reads an
object, resets Ember with explicit confirmation, asserts that owned state is
removed, and repeats creation in the same disposable state directory. The
fixture is temporary, mode `0700`, and contains no secret values.

For an explicit reset of a state directory:

```text
ember --state-dir /path/to/state reset --confirm
```

Reset is intentionally confirmation-gated and removes only Ember-owned state.

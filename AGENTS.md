# MachineProxy
You do not need to read `README.md`; it is for humans.

## Tech Stack
- Use Go for everything, and build with Goreleaser.
- All machine-to-machine protocols should use CBOR serialization.
- Containers use Bubblewrap. Use `man bwrap` to read its usage.

## Code Style
- Use comments to document higher-level intent.

## Compilation
Always perform a full rebuild with `goreleaser build --snapshot --clean`, and use the artifacts under `dist/`.

## Glossary
### Environments
- Local: The operator's OS. `machineproxy` runs here.
- Container: A Bubblewrap-created mount namespace on the Local machine. The target process, `mproxy-tracer`, and `mproxy-shim` run here. The remote workspace is mounted locally via FUSE/SFTP and bind-mounted into this namespace.
- Remote: The SSH-accessible machine where non-whitelisted execs from the target process run. `mproxy-agent` runs here.

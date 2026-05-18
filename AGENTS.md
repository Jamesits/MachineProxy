# MachineProxy
You do not need to read `README.md`; it is for humans.

## Scope
Run a program locally while it accesses files and executes commands on a remote machine over SSH.

## Tech Stack
- Use Go for everything, and build with Goreleaser
- All machine-to-machine protocols should use CBOR serialization
- Containers use Bubblewrap. Use `man bwrap` to read its usage

## Code Style
- Use comments to document higher-level intent
- Keep `cmd/*` lean, organize features into packages
- Avoid using `context.Background()` and `context.TODO()` in packages, use the context from caller
- Run `go vet ./...` (must be run in `GOOS`/`GOARCH` matrix), `golangci-lint run` and `go fmt ./...` after code change

## Compilation
Always perform a full rebuild with `goreleaser build --snapshot --clean`, and use the artifacts under `dist/`.

## Glossary
### Environments
- Local: The user's workstation OS. `machineproxy` runs here.
- Container: A Bubblewrap-created mount namespace on the Local machine. The target process, `mproxy-tracer`, and `mproxy-shim` run here. The remote workspace is mounted locally via FUSE/SFTP and bind-mounted into this namespace.
- Remote: The SSH-accessible machine where non-whitelisted execs from the target process run. `mproxy-agent` runs here.

# MachineProxy
You do not need to read `README.md`; it is for humans.

## Scope
Run a program locally while it accesses files and executes commands on a remote machine over SSH.

## Tech Stack
- Use Go for everything, and build with Goreleaser
- All machine-to-machine protocols should use CBOR serialization
- Containers use Bubblewrap. Use `man bwrap` to read its usage

## Code Style
- Keep the feature parity of the tracer and the interposer
- Use comments to document higher-level intent
- Keep `cmd/*` lean, organize features into packages
- Avoid using `context.Background()`, `context.TODO()` or `nil` context in packages, use the context from caller
- Every command line argument must have its counterpart in the config; after modifying the config structure, `config/*.example.toml` must be updated too
- Use `log/slog` for logging; always pass the logger from upstream to downstream, never use your own logger in the package; if logged arguments contain slices, wrap it with `logging.JSONValue` to keep spaces visible
- Run `go vet -tags backend_docker,backend_ssh ./...` (must be run in `GOOS`/`GOARCH` matrix), `golangci-lint run` and `go fmt ./...` after code change

## Compilation
Always perform a full rebuild with `goreleaser build --snapshot --clean`, and use the artifacts under `dist/`.

## Glossary
### Components
- `machineproxy`: the user-facing program that chainloads the target program
- tracer
  - `mproxy-tracer` (Linux only): ptrace-based syscall interceptor for the target program; runs inside the Container
  - `darwin/interposer` (macOS only): a C dylib injected via `DYLD_INSERT_LIBRARIES`; libc-level exec hook that replaces `mproxy-tracer` on macOS; built by `darwin/interposer/build.sh`, not the Go build system
- `mproxy-shim`: launched inside the Container in lieu of the intended program; proxies signals and FDs to the remote side
- `mproxy-agent`: launched on the Remote to wrap the intended program; proxies signals and FDs to the local side

### Environments
- Local: The user's workstation OS. `machineproxy` runs here.
- Container
  - Linux: A Bubblewrap-created mount namespace on the Local machine. The target process, `mproxy-tracer`, and `mproxy-shim` run here. The remote workspace is mounted locally via FUSE/SFTP and bind-mounted into this namespace.
  - macOS: `machineproxy` launches the target process directly, and exec interception is handled by `darwin/interposer` instead.
- Remote: The SSH-accessible machine where non-whitelisted execs from the target process run. `mproxy-agent` runs here.

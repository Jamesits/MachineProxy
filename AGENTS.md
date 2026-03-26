# MachineProxy
No need to read `README.md`, it is for humans.

## Tech Stack
- Use Golang for everything, build with Goreleaser
- All serialization should use CBOR
- Container use Bubblewrap (use `man bwrap` to read usage)

## Code Style
- Document the higher intention with comments

## Glossary
### Environments
- Local: The operator's OS. `machineproxy` runs here.
- Container: A new mount namespace where the target process and `mproxy-shim` runs. It is on the same machine as Local. It has the remote workspace mounted as FUSE.
- Remote: The machine where new programs invoked by the target process runs. Not the same machine of Local and Container. `mproxy-agent` runs here.

# MachineProxy

MachineProxy creates a mixed-reality environment for your agent, or for any other program. The program itself runs locally while seeing and acting on another machine over SSH. Harness features and tools work transparently.
It solves the last-hop problem for your AI agent, even when the target machine is too outdated to install the agent on or has no Internet access.

![Project Status - Premature](https://img.shields.io/badge/Project_Status-Premature-yellow)
![100% AI Code](https://img.shields.io/badge/AI_Code-100%25-blue)
![Reviewed by a Human](https://img.shields.io/badge/Reviewed_by-a_Human-green)
[![Go Reference](https://pkg.go.dev/badge/github.com/jamesits/machineproxy.svg)](https://pkg.go.dev/github.com/jamesits/machineproxy)

## Usage

Install [the latest package](https://github.com/Jamesits/MachineProxy/releases/latest) with your package manager of choice.

Create an empty directory on your local workstation at the same path as the remote workspace. If you can't, use `-v "$(pwd):/path/to/remote/workspace"` to map a different local path.

Then run it:

```shell
cd /path/to/your/workspace/root
machineproxy [-p port] [[user@]hostname] -- <program>
```

See the [example config](/config/machineproxy.example.toml) for less common use cases, including environment variable filtering.

## Development

Requirements:

- Golang
- Goreleaser 2+
- [Bubblewrap](https://github.com/containers/bubblewrap)
- [FUSE](https://sourceforge.net/projects/fuse/)

Building:

```shell
goreleaser build --snapshot --clean
```

## FAQ

### Why

Common CLI-based AI coding agents assume they are running on the same device as the workspace. That assumption is not always true:

- Some threat models forbid running an AI coding agent directly on certain devices
- Some target devices cannot run modern, resource-heavy Node.js programs
- Some environments require properly packaged software instead of "easy-to-use" one-line install commands

This program aims to work around these problems.

### Why not tell the AI agent to just use SSH?

MachineProxy pros:

- `@file` works
- All native tools (read/write files, searching, executing programs or shell commands, etc.) work
- Harness-local file/code indexing works
- Local executables can call remote executables, and they can pipe data between each other

"Just use SSH" pros:

- Broader compatibility

### How

The program is launched with a quasi-remote environment.

- The workspace is mounted with FUSE over SFTP
- Child processes are intercepted and launched over SSH

### Compatibility, or how good it is

MachineProxy supports Linux only, for both local and remote devices. Golang runtime requires Linux 3.2 or later; [support differs on different architectures](https://go.dev/wiki/MinimumRequirements#linuxlinux).

The MachineProxy remote agent (`mproxy-agent`) must be compiled for the remote device's architecture. The official packages contain `amd64` (v1) and `arm64` (v8) builds. If you need agents for other architectures or variants, you must compile them yourself. I can't test those builds because I don't have the hardware, so bugs might exist; bug reports and contributions are welcome.

MachineProxy is designed to work with most programs, including editors and CLI-based AI coding agents. Most use cases are covered, but edge cases exist and some may be impossible to fix completely.

### Security

DO NOT treat MachineProxy as a security barrier. Programs launched by MachineProxy can run commands on both the local and remote devices. Only run programs you trust, and only ask the AI to do things you trust it to do.

## Known Issues

### Mounts

- DO NOT mount over your local home directory, otherwise your AI agents might not be able to read their config.

### Environment Variables Filtering

- DO NOT filter `MPROXY_*` in `container.env_remove`

### VSCode Terminal

VSCode Terminal seems to override `/usr/bin/env node` in some cases, causing some programs, such as [Amp](https://ampcode.com), to fail to launch. If you are using MachineProxy from a VSCode Terminal, run Node.js programs like this, using `amp` as an example:

```shell
machineproxy [other args] /usr/bin/node "$(which amp)" --no-ide
```

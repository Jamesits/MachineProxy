# MachineProxy

Solves the last hop problem for your AI agent, no matter if its target is outdated or have no Internet access.

![Project Status - Premature](https://img.shields.io/badge/Project_Status-Premature-yellow)
![100% AI Code](https://img.shields.io/badge/AI_Code-100%25-blue)

MachineProxy creates a mixed reality environment for your agent (or any program). The program itself runs locally, while it sees and acts on another machine over SSH.

## Usage

```shell
machineproxy --config <./config.yaml> <program>
```

## Building

Requirements:

- Golang
- Goreleaser 2+
- [Bubblewrap](https://github.com/containers/bubblewrap)
- FUSE

Building:

```shell
goreleaser build --snapshot --clean
```

## FAQ

### Why

Common CLI-based AI coding agents assume that it is running on the same device of the workspace. But this assumption is not always true:

- Some threat models forbid running an AI coding agent directly on certain devices
- Some target devices are not qualified to run these modern, shiny NodeJS programs
- Some environments do not work with software without proper packaging that come with "easy to use" one-liner install commands

This program aims to work around these problems.

### How

The program is launched with a quasi-remote environment.

- The workspace is mounted with FUSE over SFTP
- Child processes are intercepted and launched over SSH

### Compatibility

This program is designed to work with most other programs, including and not limited to editors and CLI-based AI coding agents.

This is dirty job. All common use cases are covered, but edge cases do exist and we cannot fix them all in theory.

### Security

DO NOT treat MachineProxy as a security barrier. The program launched by MachineProxy can run programs on both the local and remote device. Only run programs you trust, and only tell the AI to do what you trust it to do.

#### Known Issues

##### Shells

Some shell (`sh`, Bash, etc.) maintains a command cache for quick lookup of commands in the PATH. So if a command exists at the remote device but does not exist at the local device, it refuses to launch it. Use the full path (`/usr/bin/...`) to bypass the command cache instead.

##### VSCode Terminal

VSCode terminal seems to be overriding `/usr/bin/env node` for some reason, causing some programs (e.g. amp as in ampcode.com) fail to launch. If you are using MachineProxy from a VSCode terminal, run node programs like this (use `amp` as an example):

```shell
machineproxy [...args] /usr/bin/node "$(which amp)" --no-ide
```

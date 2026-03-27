# MachineProxy

This is Mixed Reality for programs. Run a local program with a quasi-remote environment. The program is launched locally. File IO to specific directories and subprocesses are redirected to the remote device.

![Project Status - Development](https://img.shields.io/badge/Project_Status-Development-red)
![100% AI Code](https://img.shields.io/badge/AI_Code-100%25-blue)

Caution: this project is at a very early stage of development. **USE AT YOUR OWN RISK.**

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

With a lot hooks, obviously. The workspace is mounted with FUSE over SFTP, so the launched program thinks the files exist. Child processes are intercepted and launched over SSH instead.

### Compatibility

This program is designed to work with most other programs, including and not limited to editors and CLI-based AI coding agents.

This is dirty job. Edge cases exist and we cannot fix them all in theory.

#### Known Issues

##### Bash

Bash maintains a command cache for quick lookup of commands in the PATH. So if a command exists at the remote device but does not exist at the local device, it refuses to launch it. Use the full path (`/usr/bin/...`) to bypass the command cache instead.

##### VSCode Terminal

VSCode terminal seems to be overriding `/usr/bin/env node` for some reason, causing some programs (e.g. amp as in ampcode.com) fail to launch. Use the following command instead:

```shell
machineproxy [...args] /usr/bin/node "$(which amp)" --no-ide
```

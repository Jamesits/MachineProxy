# MachineProxy

This is Mixed Reality for programs. Run a local program with a quasi-remote environment. The program is launched locally. File IO to specific directories and subprocesses are redirected to the remote device.

![Project Status - Development](https://img.shields.io/badge/Project_Status-Development-red)
![100% AI Code](https://img.shields.io/badge/AI_Code-100%25-blue)

Caution: this project is at a very early stage of development. **USE AT YOUR OWN RISK.**

## Usage

```shell
sudo MPROXY_HOOK_LIB="$(pwd)/hook/libmproxyhook.so" ./dist/machineproxy_linux_amd64_v1/machineproxy --config ./config.yaml claude
```

## Building

Requirements:

- Golang
- Goreleaser 2+
- [Bubblewrap](https://github.com/containers/bubblewrap)

Building:

```shell
goreleaser build --snapshot --clean
```

## FAQ

### Why

Common CLI-based AI coding agents assume that it is running on the same device of the workspace. But this assumption is not always true:

- Some threat model forbids running an AI coding agent directly on certain devices
- Some target devices are not qualified to run these modern, shiny NodeJS programs
- Some environment does not work with software without proper packaging that come with "easy to use" one-liner install commands

This program aims to work around these problems.

### How

With a lot hooks, obviously.

### Compatibility

This program is designed to work with most other programs, including and not limited to editors and CLI-based AI coding agents.

This is dirty job. Edge cases exist and we cannot fix them all in theory.

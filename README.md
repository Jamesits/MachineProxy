# MachineProxy

MachineProxy creates a mixed-reality environment for your agent, or for any other program. The program itself runs locally while seeing and acting on another machine over SSH. Harness features and tools work transparently.
It solves the last-hop problem for your AI agent, even when the target machine is too outdated to install the agent on or has no Internet access.

![Project Status - Premature](https://img.shields.io/badge/Project_Status-Premature-yellow)
![100% AI Code](https://img.shields.io/badge/AI_Code-100%25-blue)
![Reviewed by a Human](https://img.shields.io/badge/Reviewed_by-a_Human-green)
[![Go Reference](https://pkg.go.dev/badge/github.com/jamesits/machineproxy.svg)](https://pkg.go.dev/github.com/jamesits/machineproxy)

## Demo

Here's a simple demo with explanation of what happened on each command. We use `bash` for better demonstration; in real life you would run a real workload (e.g. `claude`).

```shell
# Enter a MachineProxy environment.
# A bash from the workstation is executed, and everything else run by it will be sent to the remote host.
ubuntu@workstation:~/temp$ machineproxy remote.host -- bash

# Bash itself is running in the workstation, so if you instruct it to read a file, it reads the file from the workstation.
# The shell prompt does not change too because bash reads its RC file from the workstation.
# `grep` is a child process so it runs in the remote host; MachineProxy proxies the pipe between them.
ubuntu@workstation:~/temp$ echo "$(</etc/os-release)" | grep PRETTY_NAME
PRETTY_NAME="Ubuntu 26.04 LTS"

# `cat` is a child process so it runs in the remote host and reads the file from the remote host.
# `grep` run in the remote host too.
ubuntu@workstation:~/temp$ cat /etc/os-release | grep PRETTY_NAME
PRETTY_NAME="Rocky Linux 8.10 (Green Obsidian)"
```

## Installation

Install [the latest package](https://github.com/Jamesits/MachineProxy/releases/latest) with your package manager of choice.

For Ubuntu or other distros that has unprivileged user namespace disabled:

```shell
echo "kernel.apparmor_restrict_unprivileged_userns=0" | sudo tee /etc/sysctl.d/80-machineproxy.conf
sudo sysctl -p /etc/sysctl.d/80-machineproxy.conf
```

Config `container.local_commands` in `/etc/machineproxy/machineproxy.toml` according to your needs. See the [example config](/config/machineproxy.example.toml) for less common use cases, including environment variable filtering.

## Usage

### SSH

Create an empty directory on your local workstation at the same path as the remote workspace. If you can't, use `-v "$(pwd):/path/to/remote/workspace"` to map a different local path.

```shell
cd /path/to/your/workspace/root
machineproxy ssh://[user@]hostname[:port] -- <program>
# or use a more SSH-like grammar:
machineproxy [-p port] [[user@]hostname] -- <program>
```

### Docker

```shell
machineproxy -v "$(pwd):/workspace" docker://container_name -- <program>
```

Supported URL schemas:

- `docker://container_id`
- `docker://container_name`
- `compose://project/service`: Attach to a service in a Docker Compose project
- `compose://./service`: Attach to a service in the current Docker Compose project
- `compose://project/service/id`: Use this when you have multiple instances of a service

## Development

Requirements:

- Golang
- Goreleaser 2+
- [Bubblewrap](https://github.com/containers/bubblewrap) (Linux)
- [FUSE](https://sourceforge.net/projects/fuse/) (Linux) | [macFUSE](https://macfuse.io) (macOS)

Building:

```shell
goreleaser release --snapshot --clean
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

- `@file` and harness-local file/code indexing works
- All native tools (read/write files, searching, executing programs or shell commands, etc.) work
- File descriptor inheritance and pipe works across the machine boundary
- Works over both SSH and Docker

"Just use SSH" pros:

- Broader compatibility on embedded systems with a SSH server

### How

The program is launched with a quasi-remote environment.

- The workspace is mounted with FUSE over SFTP
- Child processes are intercepted and launched over SSH

### Compatibility, or how good it is

#### Local

<details>
  <summary>Linux: fully works.</summary>

Programs are launched inside a Bubblewrap container and traced via `ptrace` for transparent exec/file proxying. Golang runtime requires Linux 3.2 or later; [support differs on different architectures](https://go.dev/wiki/MinimumRequirements#linuxlinux). Runtime dependencies: `bubblewrap`, `fuse`.
</details>

<details>
  <summary>macOS: highly experimental.</summary>

Runtime dependency: [macFUSE](https://macfuse.io) (`brew install --cask macfuse`).

On macOS the host has no containment. Workspace files are exposed via macFUSE (install with `brew install --cask macfuse`) and exec interception is delivered by `libmproxy_interposer.dylib` injected via `DYLD_INSERT_LIBRARIES`.

Apple ships most `/usr/bin` tools with the hardened runtime, which strips `DYLD_INSERT_LIBRARIES` at exec time. Those binaries always run locally even without an entry in local_commands. Install Homebrew equivalents (e.g. coreutils, git, llvm) into /opt/homebrew/bin if you need them routed to the remote.

Working dir configuration is ignored: there is no bind-mount equivalent, so the FUSE mount lives at the temp path machineproxy creates at startup and the broker's path mapper rewrites that prefix to the remote workspace path automatically.

Whitelist semantics: the darwin interposer dylib supports exact-path and basename entries in local_commands; regex entries (`/pattern/`) fall through to the remote (limitation of the C implementation).

Set the env var `MPROXY_INTERPOSER_STRIP_DYLD_ON_WHITELIST=1` to make the dylib remove DYLD_INSERT_LIBRARIES from the env passed to whitelisted commands. Useful when a whitelisted process (make, bash, etc.) spawns lots of local-only subcommands and you want those subtrees to run without the interposer. Non-whitelisted execs still chain the dylib into the child as usual.
</details>

#### Remote

The remote agent (`mproxy-agent`) must be compiled for the remote device's OS and architecture. The official packages contain `amd64` (v1) and `arm64` (v8) builds. If you need agents for other architectures or variants, you must compile them yourself. I can't test those builds because I don't have the hardware, so bugs might exist; bug reports and contributions are welcome.

#### Target Program

MachineProxy is designed to work with most programs, including editors and CLI-based AI coding agents. Most use cases are covered, but edge cases exist and some may be impossible to fix completely.

### Security

DO NOT treat MachineProxy as a security barrier. Programs launched by MachineProxy can run commands on both the local and remote devices. Only run programs you trust, and only ask the AI to do things you trust it to do.

## Known Issues

### Mounts

- DO NOT mount over your local home directory, otherwise your AI agents might not be able to read their config.

### Pipe Performance

If the process setting up the pipe runs locally, and both the pipe sender and the receiver runs on the remote machine, the pipe goes through the SSH connection twice.

<details>
  <summary>Claude's explanation</summary>

```
❯ Validate a thing:
  When I run bash as the machineproxy entrypoint program, and run `cat xxx |
  grep yyy` inside the bash, does the pipe content goes roundtrip across the
  SSH or it is shortcut'd on the remote side

● Answer: Round-trips across SSH. Twice, in fact.

  Here's the actual data path for cat xxx | grep yyy when bash is the target:

  1. Bash runs in the local container. When it sets up the pipe, the pipe FDs
  are created in the local kernel.
  2. Bash forks twice. Each child execve's cat/grep. The tracer
  (pkg/tracer/tracer.go:269-273) intercepts non-whitelisted execs and rewrites
  them to launch mproxy-shim locally, inheriting the pipe ends as stdin/stdout.
  3. Each local shim connects to the broker (cmd/mproxy-shim/main.go:41-50),
  which spawns the real cat/grep on the remote via the agent. The remote child's
   stdio is connected to socketpairs, muxed back over SSH.
  4. The shim then byte-pumps between its inherited FDs and the broker
  connection (streamStdin, streamFD at cmd/mproxy-shim/main.go:168-197).

  So the bytes for cat's output travel:

  remote cat stdout
    → socketpair (remote)
    → SSH mux
    → broker (local)
    → shim_cat stdout
    → local kernel pipe          ← the only place cat and grep "meet"
    → shim_grep stdin
    → broker (local)
    → SSH mux
    → remote grep stdin

  That's two SSH traversals for the pipe content (remote→local, then
  local→remote), plus a third for grep's output back to the terminal. There is
  no remote-side shortcut — bash, which owns the pipe, is in the container, so
  the pipe necessarily exists locally.

  If you want this shortcut'd, the options would be either (a) whitelist
  cat/grep so they run locally against the FUSE-mounted workspace, or (b) keep
  the shell remote and only proxy specific operations the other way. The current
   design pays the round-trip cost as the tradeoff for keeping the shell
  environment local.

❯ Can we detect this case in the broker (2 remote pipe wants to connect) and
  let the broker tell the shim to shortcut them?

● Yes, this is detectable, but with a real timing wrinkle. Sketch:

  Detection signal. Two ends of the same pipe share a kernel inode. Each shim
  can fstat its inherited stdio/extra FDs and report (role, dev, ino, kind=pipe)
   in the ExecRequest. The broker keeps a short-lived ino → (shim, role) table;
  when shim_cat's stdout-ino matches shim_grep's stdin-ino, they're paired. No
  SCM_RIGHTS needed.

  Action. Add a new agent control message: instead of two independent stdio mux
  streams, tell the agent to create a real pipe() on the remote side and wire it
   as cat's stdout / grep's stdin. The local kernel pipe between the two shims
  becomes dormant — both shims just hold their FDs and forward nothing.

  The race. Bash forks all pipeline stages before either execs, so shim arrivals
   are nearly simultaneous in the common case — but not guaranteed. The broker
  has to decide: launch cat eagerly (and risk having to replay buffered bytes
  once grep arrives), or hold cat until its partner shows up (and risk stalling
  if grep never comes — e.g., backgrounded pipelines, slow forks, weird shells).
   A bounded delay (~50–100ms or until cat produces its first byte) covers most
  shell pipelines and degrades gracefully to the current path.

  Edge cases worth thinking about:
  - One side whitelisted/local — already cheap, skip the shortcut.
  - tee, <(...) process substitution, coproc, dup'd FDs in scripts — pipes can
  have >2 endpoints; only shortcut when exactly one writer and one reader claim
  the inode.
  - Pipe with data already buffered locally before the match is detected — needs
   replay across the new remote pipe, or just give up on shortcutting that pair.
  - Shim ordering — broker must handle either side arriving first.

  Tradeoff. Real win for data-heavy remote pipelines (eliminates 2× SSH hops for
   the pipe body). Cost is a non-trivial protocol change (shim → broker FD
  metadata, broker → agent splice command) plus the matching window logic, which
   is the kind of timing-dependent code that's easy to get subtly wrong. I'd
  recommend prototyping the detection-only path first (log when a shortcut would
   apply) to see how often it actually fires in your workloads before committing
   to the wiring.
```
</details>

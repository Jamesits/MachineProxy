#!/usr/bin/env bash
# Build libmproxy_interposer.dylib for darwin/arm64 and darwin/amd64 into
# the same dist/agent/darwin/<arch>/ layout that goreleaser uses for the
# mproxy-agent binary. Run from the repo root or via goreleaser's
# before.hooks.
#
# Skipped on non-darwin hosts: the dylib only loads on macOS, so cross-
# compiling from Linux would produce an artifact nobody can use. We do
# *not* fail in that case — goreleaser invokes the same before.hooks on
# every machine and we don't want CI to break on Linux.

set -euo pipefail

if [[ "$(uname -s)" != "Darwin" ]]; then
    echo "build-interposer.sh: skipping (host is not darwin)"
    exit 0
fi

SRC="darwin/interposer/interposer.c"
if [[ ! -f "$SRC" ]]; then
    echo "build-interposer.sh: $SRC not found; are you running from the repo root?" >&2
    exit 1
fi

CC="${CC:-clang}"
CFLAGS=(
    -Wall
    -Wextra
    -Wpedantic
    -O2
    -dynamiclib
    -fvisibility=hidden  # only the __interpose section needs to be visible
)

for arch in arm64 amd64; do
    case "$arch" in
        arm64) clang_arch=arm64 ;;
        amd64) clang_arch=x86_64 ;;
        *) echo "build-interposer.sh: unsupported arch $arch" >&2; exit 1 ;;
    esac

    # Output lives outside dist/ so it survives goreleaser's
    # distribution-directory cleanup; archive files: rules pull it in
    # at packaging time.
    out_dir="darwin/interposer/build/${arch}"
    out_file="${out_dir}/libmproxy_interposer.dylib"

    mkdir -p "$out_dir"
    "$CC" "${CFLAGS[@]}" -arch "$clang_arch" -o "$out_file" "$SRC"
    echo "build-interposer.sh: built $out_file"
done

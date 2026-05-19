#!/usr/bin/env bash
set -Eeuo pipefail

ss -ltn | grep -q ':22 ' || exit 1

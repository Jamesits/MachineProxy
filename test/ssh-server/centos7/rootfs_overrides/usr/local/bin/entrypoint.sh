#!/usr/bin/env bash
set -Eeuo pipefail

# Generate machine key
ssh-keygen -A

# Set authorized key
echo "${AUTHORIZED_KEY}" | install -m 0600 -o 0 -g 0 /dev/stdin /root/.ssh/authorized_keys

# CentOS 7 OpenSSH 7.4 does not support Include directive,
# so append the test config directly to sshd_config
cat /etc/ssh/sshd_config.d/*.conf >> /etc/ssh/sshd_config

# Validate sshd config
/usr/sbin/sshd -T || exit 1

# Start sshd
exec /usr/sbin/sshd -D -e

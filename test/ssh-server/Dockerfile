FROM debian:13-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends openssh-server bash procps iproute2 \
    && rm -rf /etc/ssh/ssh_host_* \
    && rm -rf /var/lib/apt/lists/*

COPY --chown=0:0 rootfs_overrides/. /

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
HEALTHCHECK --interval=5s --timeout=3s --start-period=3s --retries=3 CMD /usr/local/bin/healthcheck.sh
EXPOSE 22/tcp

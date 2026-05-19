FROM centos:7

# Point to vault since CentOS 7 is EOL
RUN sed -i 's|^mirrorlist=|#mirrorlist=|g' /etc/yum.repos.d/CentOS-*.repo \
    && sed -i 's|^#baseurl=http://mirror.centos.org|baseurl=http://vault.centos.org|g' /etc/yum.repos.d/CentOS-*.repo \
    && yum install -y openssh-server bash procps iproute \
    && rm -rf /etc/ssh/ssh_host_* \
    && yum clean all

# Ensure root .ssh directory exists
RUN mkdir -p /root/.ssh && chmod 700 /root/.ssh

COPY --chown=0:0 rootfs_overrides/. /

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
HEALTHCHECK --interval=5s --timeout=3s --start-period=3s --retries=3 CMD /usr/local/bin/healthcheck.sh
EXPOSE 22/tcp

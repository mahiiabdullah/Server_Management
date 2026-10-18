# A throwaway "server" for demonstrating the SSH access workflow on one machine.
# Real OpenSSH, configured by the real deploy/ssh-access/setup-server.sh.
FROM ubuntu:24.04
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends openssh-server sudo procps \
 && rm -rf /var/lib/apt/lists/* \
 && mkdir -p /run/sshd \
 && printf '#!/bin/sh\n# No systemd in a container: "reload" sshd by signal.\npkill -HUP -x sshd; exit 0\n' > /usr/local/bin/systemctl \
 && chmod +x /usr/local/bin/systemctl
COPY deploy/ssh-access /opt/ssh-access
COPY deploy/access-demo/server-entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh /opt/ssh-access/*.sh /opt/ssh-access/bm-access-apply
EXPOSE 22
ENTRYPOINT ["/entrypoint.sh"]

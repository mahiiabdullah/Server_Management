# Keeps SSH local-forwards open to each server's Node Exporter (127.0.0.1:9100), so a
# monitoring stack that is not on the servers' network can scrape them without any
# port being opened on the servers. The key it uses is restricted server-side to
# permitopen="127.0.0.1:9100" with no shell (see docs/RUNBOOK.md).
FROM alpine:3.22
RUN apk add --no-cache openssh-client
COPY entrypoint.sh /entrypoint.sh
RUN chmod 755 /entrypoint.sh
ENTRYPOINT ["/entrypoint.sh"]

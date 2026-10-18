# A GitHub Actions self-hosted runner (label bm-access) for the SSH access demo.
# It runs the repository's access-apply workflow; the CA and deploy keys are in
# its volume at /etc/bm-access, never in GitHub.
FROM ghcr.io/actions/actions-runner:latest
USER root
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends openssh-client git ca-certificates \
 && rm -rf /var/lib/apt/lists/*
COPY deploy/access-demo/runner-entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
USER runner
ENTRYPOINT ["/entrypoint.sh"]

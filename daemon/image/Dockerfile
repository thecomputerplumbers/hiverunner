# The guest every job runs in. Built with Docker on the Mac and turned into a
# bootable disk by build.sh; it is never run as a container.
FROM --platform=linux/arm64 ubuntu:24.04

ARG RUNNER_VERSION
ARG RUNNER_SHA256
ENV DEBIAN_FRONTEND=noninteractive

# A kernel and init, and what a job expects to find: git, curl, sudo, the
# usual archive tools. Deliberately not the hosted runners' toolcache; jobs
# install what they need with setup-* actions, as they do anywhere.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      systemd systemd-sysv systemd-resolved dbus udev kmod \
      linux-image-virtual initramfs-tools e2fsprogs \
      ca-certificates curl git sudo iproute2 iputils-ping \
      jq tar gzip unzip zip xz-utils zstd openssh-client locales socat \
 && locale-gen en_US.UTF-8 \
 && rm -rf /var/lib/apt/lists/*

# GitHub's own runner, pinned and verified against the hash in its release.
RUN useradd --create-home --uid 1001 --shell /bin/bash runner \
 && echo 'runner ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/runner \
 && chmod 0440 /etc/sudoers.d/runner \
 && mkdir -p /opt/runner \
 && curl -fsSL -o /tmp/runner.tar.gz \
      "https://github.com/actions/runner/releases/download/v${RUNNER_VERSION}/actions-runner-linux-arm64-${RUNNER_VERSION}.tar.gz" \
 && echo "${RUNNER_SHA256}  /tmp/runner.tar.gz" | sha256sum -c - \
 && tar -xzf /tmp/runner.tar.gz -C /opt/runner \
 && rm /tmp/runner.tar.gz \
 && apt-get update \
 && /opt/runner/bin/installdependencies.sh \
 && rm -rf /var/lib/apt/lists/* \
 && chown -R runner:runner /opt/runner

# The runner starts every JavaScript action with one of its own copies of
# Node. Each is put behind hive-node, which points the action at the host's
# cache when there is one and otherwise changes nothing. The build fails if
# the runner stops shipping Node where this expects it, rather than produce
# an image whose jobs silently never use the cache.
COPY hive-node /usr/local/lib/hive-node
RUN chmod 0755 /usr/local/lib/hive-node \
 && wrapped=0 \
 && for node in /opt/runner/externals/node*/bin/node; do \
      case "$node" in *_alpine/*) continue ;; esac; \
      [ -x "$node" ] && [ ! -L "$node" ] || { echo "unexpected $node" >&2; exit 1; }; \
      mv "$node" "$node.real" \
      && cp /usr/local/lib/hive-node "$node" \
      && chown runner:runner "$node" "$node.real" \
      && "$node" --version >/dev/null \
      && wrapped=$((wrapped + 1)); \
    done \
 && [ "$wrapped" -ge 1 ] \
 && echo "Wrapped $wrapped copies of Node"

COPY hive-agent /usr/local/sbin/hive-agent
COPY hive-agent.service /etc/systemd/system/hive-agent.service
COPY 10-dhcp.network /etc/systemd/network/10-dhcp.network

RUN chmod 0755 /usr/local/sbin/hive-agent \
 && systemctl enable hive-agent.service systemd-networkd.service systemd-resolved.service \
 # Nothing listens and nobody logs in: there is no way into the guest but
 # the job itself.
 && systemctl mask getty@.service serial-getty@.service console-getty.service \
 && passwd -l root \
 && echo "RUNNER_VERSION=${RUNNER_VERSION}" > /etc/hive-release

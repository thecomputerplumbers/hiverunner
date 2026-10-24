# The guest every job runs in. Built with Docker and turned into a bootable
# disk by build.sh, which also chooses the architecture; it is never run as a
# container.
FROM ubuntu:24.04

ARG RUNNER_VERSION
ARG RUNNER_SHA256
# GitHub's name for the runner's architecture: arm64 or x64.
ARG RUNNER_ARCH=arm64
ENV DEBIAN_FRONTEND=noninteractive

# A kernel and init, and what a job expects to find: git, curl, sudo, the
# usual archive tools, and libatomic, which Node's own arm64 builds load.
# Deliberately not the hosted runners' toolcache; jobs install what they need
# with setup-* actions, as they do anywhere.
#
# linux-image-virtual is the small VM kernel. zram (compressed swap) lives
# in that kernel's extra-modules package, which the virtual image leaves
# out. The extra package is here so a job that uses a lot of RAM can swap
# instead of being killed; hive-agent loads the module at boot.
#
# Python is the exception, and only the system's own: workflows and actions
# call `python3` for a few lines of glue as freely as they call jq, and
# found none here. It is Ubuntu's 3.12 with venv, and no pip outside a venv;
# a job that wants a particular Python still asks setup-python for it. The
# build fails if it cannot read TOML or make a venv with pip in it, which is
# what that glue does.
#
# So is a C and C++ compiler. GitHub's runners have one, and much that never
# says "C" needs it: cgo (`go test -race` refuses to run without it), the
# linker Rust's toolchain calls as `cc`, node-gyp, and Python packages built
# from source. It is gcc, g++, make and libc6-dev, which is build-essential
# without dpkg-dev, about 220 MB. The build fails if it cannot compile and
# run a C++ program.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      systemd systemd-sysv systemd-resolved dbus udev kmod \
      linux-image-virtual initramfs-tools e2fsprogs \
      ca-certificates curl git sudo iproute2 iputils-ping \
      jq tar gzip unzip zip xz-utils zstd openssh-client locales socat \
      libatomic1 python3 python3-venv \
      gcc g++ make libc6-dev \
 && kernel=$(ls /lib/modules | sort -V | tail -1) \
 && { apt-get install -y --no-install-recommends "linux-modules-extra-$kernel" \
      || apt-get install -y --no-install-recommends linux-image-extra-virtual; } \
 && locale-gen en_US.UTF-8 \
 && rm -rf /var/lib/apt/lists/* \
 && python3 -c 'import json, sqlite3, ssl, tomllib' \
 && python3 -m venv /tmp/venv && /tmp/venv/bin/pip --version && rm -rf /tmp/venv \
 && printf '#include <iostream>\nint main() { std::cout << "ok"; }\n' > /tmp/t.cc \
 && c++ /tmp/t.cc -o /tmp/t && [ "$(/tmp/t)" = ok ] && rm /tmp/t /tmp/t.cc \
 && cc --version >/dev/null && make --version >/dev/null

# GitHub's own runner, pinned and verified against the hash in its release.
RUN useradd --create-home --uid 1001 --shell /bin/bash runner \
 && echo 'runner ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/runner \
 && chmod 0440 /etc/sudoers.d/runner \
 && mkdir -p /opt/runner \
 && curl -fsSL -o /tmp/runner.tar.gz \
      "https://github.com/actions/runner/releases/download/v${RUNNER_VERSION}/actions-runner-linux-${RUNNER_ARCH}-${RUNNER_VERSION}.tar.gz" \
 && echo "${RUNNER_SHA256}  /tmp/runner.tar.gz" | sha256sum -c - \
 && tar -xzf /tmp/runner.tar.gz -C /opt/runner \
 && rm /tmp/runner.tar.gz \
 && apt-get update \
 && /opt/runner/bin/installdependencies.sh \
 && rm -rf /var/lib/apt/lists/* \
 && chown -R runner:runner /opt/runner

# Docker, for jobs that build images or use `container:` and `services:`: the
# engine, its command, and the buildx and compose plugins. From Docker's own
# repository, which is where GitHub's runners get theirs, so a job that moves
# here finds the Docker it had. Ubuntu packages all four as well, some
# releases behind and compose a whole major version behind.
#
# Nothing starts at boot. The socket is there for the runner's user, and the
# engine starts when something first speaks to it, so a job that never uses
# Docker does not wait for it or give it memory. busybox is the self-test's
# container: one static file, so the test pulls nothing.
#
# linux-image-virtual has what Docker needs of the kernel: overlayfs, bridges
# and veth pairs, and the netfilter pieces iptables-nft drives. zram comes
# from linux-modules-extra for that same kernel ABI, installed above. The
# build fails if a kernel ever moves one of them out, rather than produce an
# image whose containers have no network or whose jobs have no compressed
# swap.
RUN install -m 0755 -d /etc/apt/keyrings \
 && curl -fsSL -o /etc/apt/keyrings/docker.asc https://download.docker.com/linux/ubuntu/gpg \
 && echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo "$VERSION_CODENAME") stable" \
      > /etc/apt/sources.list.d/docker.list \
 && apt-get update \
 && apt-get install -y --no-install-recommends \
      docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin \
      busybox-static \
 && rm -rf /var/lib/apt/lists/* \
 && usermod -aG docker runner \
 && systemctl disable docker.service containerd.service \
 && systemctl enable docker.socket \
 && docker --version && docker buildx version && docker compose version \
 && kernel=$(ls /lib/modules | sort -V | tail -1) \
 && for module in overlay bridge veth br_netfilter nf_tables nft_compat nft_chain_nat \
      nf_nat nf_conntrack xt_MASQUERADE xt_addrtype xt_conntrack zram; do \
      modinfo -k "$kernel" "$module" >/dev/null || { echo "the kernel has no $module" >&2; exit 1; }; \
    done

# What a headless browser needs of the system, so a job's browser tests do
# not install it themselves. Playwright's `install --with-deps chromium` runs
# apt as root on every job for about 94 packages: 36 MB of package lists and
# 325 MB written, the heaviest moment of the job, and on hive VMs that step
# crashed (exit 139) or hung until the job timed out. These are Playwright's
# own lists for Ubuntu 24.04, chromium and tools, the same on arm64 and x64;
# with them in place `playwright install-deps --dry-run chromium` finds
# nothing to do. The browser itself is not here: its revision changes with
# every Playwright release, and a job caches the one it pins.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      libasound2t64 libatk-bridge2.0-0t64 libatk1.0-0t64 libatspi2.0-0t64 libcairo2 \
      libcups2t64 libdbus-1-3 libdrm2 libgbm1 libglib2.0-0t64 libnspr4 libnss3 \
      libpango-1.0-0 libx11-6 libxcb1 libxcomposite1 libxdamage1 libxext6 libxfixes3 \
      libxkbcommon0 libxrandr2 \
      xvfb fonts-noto-color-emoji fonts-unifont libfontconfig1 libfreetype6 \
      xfonts-cyrillic xfonts-scalable fonts-liberation fonts-ipafont-gothic \
      fonts-wqy-zenhei fonts-tlwg-loma-otf fonts-freefont-ttf \
 && rm -rf /var/lib/apt/lists/*

# The runner starts every JavaScript action with one of its own copies of
# Node. Each is put behind hive-node, which points the action at the host's
# cache when there is one and otherwise changes nothing. The build fails if
# the runner stops shipping Node where this expects it, or if an action's
# inputs would not survive the trip, rather than produce an image whose jobs
# silently never use the cache or, worse, lose their inputs.
COPY hive-node /usr/local/lib/hive-node
RUN chmod 0755 /usr/local/lib/hive-node \
 && found=0 && wrapped=0 \
 && for node in /opt/runner/externals/node*/bin/node; do \
      case "$node" in *_alpine/*) continue ;; esac; \
      [ -x "$node" ] && [ ! -L "$node" ] || { echo "unexpected $node" >&2; exit 1; }; \
      found=$((found + 1)); \
      mv "$node" "$node.real" \
      && cp /usr/local/lib/hive-node "$node" \
      && chown runner:runner "$node" "$node.real" \
      && "$node" --version >/dev/null \
      && [ "$(env 'INPUT_WITH-HYPHEN=kept' "$node" -p 'process.env["INPUT_WITH-HYPHEN"]')" = kept ] \
      && wrapped=$((wrapped + 1)); \
    done \
 && [ "$wrapped" -ge 1 ] && [ "$wrapped" -eq "$found" ] \
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

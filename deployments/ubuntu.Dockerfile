# KERN base job image — ubuntu:24.04 with the baseline toolset GitHub-hosted
# runners ship. Workflows written for `runs-on: ubuntu-latest` assume curl,
# make, gcc, jq, sudo and friends exist without a setup step; a bare
# ubuntu:24.04 fails them with `command not found`.
#
# Deliberately NOT included: language toolchains (Go, Node, Rust, Android SDK).
# Those are installed by setup-* actions, which keep working and stay pinned by
# the workflow. This image only covers the always-expected POSIX/CI baseline.
FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl wget \
    git git-lfs openssh-client rsync \
    jq unzip zip xz-utils bzip2 zstd tar file \
    build-essential make cmake pkg-config autoconf automake libtool bison flex \
    libssl-dev zlib1g-dev libffi-dev libsqlite3-dev libxml2-dev libyaml-dev \
    python3 python3-pip python3-venv python3-dev \
    sudo gnupg apt-transport-https software-properties-common \
    iproute2 net-tools dnsutils netcat-openbsd iputils-ping \
    lsof procps psmisc strace tree less vim-tiny \
    libxml2-utils sqlite3 \
    && rm -rf /var/lib/apt/lists/*

# GitHub's runner images ship `gh`; workflows call it without installing.
RUN mkdir -p /etc/apt/keyrings \
    && curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
       -o /etc/apt/keyrings/githubcli-archive-keyring.gpg \
    && chmod go+r /etc/apt/keyrings/githubcli-archive-keyring.gpg \
    && echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
       > /etc/apt/sources.list.d/github-cli.list \
    && apt-get update && apt-get install -y --no-install-recommends gh \
    && rm -rf /var/lib/apt/lists/*

# git-lfs needs its smudge filter registered for checkouts that use it.
RUN git lfs install --system || true

# Job steps run as an unprivileged uid unless the run is trusted; the workspace
# and tool caches are mounted from the host, so keep the default umask friendly.
RUN chmod -R a+rwX /usr/local/bin /opt 2>/dev/null || true

CMD ["/bin/bash"]

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
    libatomic1 libstdc++6 libgcc-s1 \
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

# Playwright's Ubuntu 24.04 dependency set (tools + chromium + firefox +
# webkit, from playwright's nativeDeps for noble) so `npx playwright install
# --with-deps` and `install-deps` are no-ops instead of per-job apt installs.
# Trusted lanes run as root and could apt these; untrusted lanes cannot
# elevate at all (read-only rootfs, no-new-privileges), so the libs must live
# in the image (#156).
RUN apt-get update && apt-get install -y --no-install-recommends \
    xvfb fonts-noto-color-emoji fonts-unifont fonts-liberation \
    fonts-ipafont-gothic fonts-wqy-zenhei fonts-tlwg-loma-otf \
    fonts-freefont-ttf xfonts-cyrillic xfonts-scalable \
    libfontconfig1 libfreetype6 \
    libasound2t64 libatk-bridge2.0-0t64 libatk1.0-0t64 libatspi2.0-0t64 \
    libcairo2 libcairo-gobject2 libcups2t64 libdbus-1-3 libdrm2 libgbm1 \
    libglib2.0-0t64 libnspr4 libnss3 libpango-1.0-0 libpangocairo-1.0-0 \
    libx11-6 libx11-xcb1 libxcb1 libxcb-shm0 libxcomposite1 libxcursor1 \
    libxdamage1 libxext6 libxfixes3 libxi6 libxkbcommon0 libxrandr2 \
    libxrender1 libgdk-pixbuf-2.0-0 libgtk-3-0t64 \
    gstreamer1.0-libav gstreamer1.0-plugins-bad gstreamer1.0-plugins-base \
    gstreamer1.0-plugins-good libgstreamer1.0-0 libgstreamer-gl1.0-0 \
    libgstreamer-plugins-bad1.0-0 libgstreamer-plugins-base1.0-0 \
    libgtk-4-1 libicu74 libenchant-2-2 libepoxy0 libevent-2.1-7t64 \
    libflite1 libgles2 libharfbuzz-icu0 libharfbuzz0b libhyphen0 \
    libjpeg-turbo8 liblcms2-2 libmanette-0.2-0 libopus0 libpng16-16t64 \
    libsecret-1-0 libvpx9 libwayland-client0 libwayland-egl1 \
    libwayland-server0 libwebp7 libwebpdemux2 libwoff1 libxslt1.1 \
    libx264-164 libavif16 \
    && rm -rf /var/lib/apt/lists/*

# Tauri build deps every release lane installs per job (Invico, Primora,
# Cal, SwingMusic, WallMotion), plus the small recurring one-offs
# (ployz minisign/qemu-user, tdvorak-fullstack shellcheck, SwingMusic libev).
# libappindicator3-dev is omitted on purpose — it conflicts with the ayatana
# fork and is the unmaintained lineage; lanes pinning it self-install.
RUN apt-get update && apt-get install -y --no-install-recommends \
    libwebkit2gtk-4.1-dev libgtk-3-dev libayatana-appindicator3-dev \
    librsvg2-dev patchelf desktop-file-utils \
    minisign qemu-user shellcheck libev-dev \
    && rm -rf /var/lib/apt/lists/*

# GitHub-hosted runner images ship Chrome; chromedp/headless-Chrome workloads
# (PDF rendering, browser smoke lanes) resolve it via CHROME_BIN or the
# default PATH lookup and fail without it (#149). Recommends stay on — the
# font packages matter for non-ASCII document rendering. No official arm64
# .deb exists, so arm64 keeps the browserless image; arm64 jobs that need
# Chrome must pin their own container image.
RUN if [ "$(dpkg --print-architecture)" = "amd64" ]; then \
      apt-get update \
      && wget -q https://dl.google.com/linux/direct/google-chrome-stable_current_amd64.deb -O /tmp/chrome.deb \
      && apt-get install -y /tmp/chrome.deb \
      && rm -f /tmp/chrome.deb \
      && rm -rf /var/lib/apt/lists/*; \
    fi

# Job steps run as an unprivileged uid unless the run is trusted; the workspace
# and tool caches are mounted from the host, so keep the default umask friendly.
RUN chmod -R a+rwX /usr/local/bin /opt 2>/dev/null || true

CMD ["/bin/bash"]

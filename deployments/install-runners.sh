#!/usr/bin/env bash
# install-runners.sh — provision one GitHub Actions Runner.Listener agent
# on an LXC host. Runs as root inside the CT; the agent lands at
# /opt/gha/<repo>/ behind its svc.sh systemd unit. Invoke once per repo.
#
#   CT=902 REPO=Dvorinka/foo RUNNER_TOKEN=<token> ./install-runners.sh
#
# The token comes from `gh api -X POST repos/<repo>/actions/runners/
# registration-token --jq .token` and lives only in the environment —
# never in this file.
#
# Labels: every agent registers self-hosted,linux,x64 plus lxc-<CT> from
# the required CT variable — never a hardcoded foreign label. The
# predecessor script registered 902 and 906 as lxc-901, so GitHub routed
# lxc-901 jobs to any host in the fleet. Capability labels go through
# EXTRA_LABELS="gpu,tauri".
#
# Host toolset: /root/host-toolset (override with HOST_TOOLSET=), one
# command name per line, is consumed before registration. Curated tools
# get a release-tarball install; anything else resolves through apt. The
# same names belong in kern's `toolset` org setting so post-provision
# drift is reported instead of silently reintroduced.
set -euo pipefail

CT="${CT:?set CT=<proxmox ctid, e.g. 902>}"
REPO="${REPO:?set REPO=owner/name}"
RUNNER_TOKEN="${RUNNER_TOKEN:?set RUNNER_TOKEN=<registration token>}"
RUNNER_VERSION="${RUNNER_VERSION:-2.338.2}"
EXTRA_LABELS="${EXTRA_LABELS:-}"
HOST_TOOLSET="${HOST_TOOLSET:-/root/host-toolset}"
AGENT_DIR="/opt/gha/${REPO##*/}"
LABELS="self-hosted,linux,x64,lxc-${CT}${EXTRA_LABELS:+,${EXTRA_LABELS}}"

install_tool() {
  case "$1" in
    # Curated: not in Debian/Ubuntu main repos at a usable version.
    # Pinned version + checked-in sha256 — an unverified mutable "latest"
    # tarball would be remote code execution on every provisioned host.
    gh)
      arch=$(uname -m)
      case "$arch" in
        x86_64)        a=amd64; s=98cb101ad87dbbd3f6b2f2bfcbd43197ccd07643140121d8a9cfaa91eadbc24e ;;
        aarch64|arm64) a=arm64; s=cb34cefc6f4e77b90731374a2b737516bb8f3033c6ca46aa949e5344f68069e3 ;;
        *) echo "unsupported arch $arch" >&2; exit 1 ;;
      esac
      tmp=$(mktemp -d)
      curl -fsSL "https://github.com/cli/cli/releases/download/v2.104.0/gh_2.104.0_linux_${a}.tar.gz" -o "$tmp/gh.tgz"
      echo "$s  $tmp/gh.tgz" | sha256sum -c -
      tar -xzf "$tmp/gh.tgz" -C "$tmp"
      install -m0755 "$tmp"/gh_*/bin/gh /usr/local/bin/gh
      rm -rf "$tmp"
      ;;
    minisign)
      arch=$(uname -m)
      case "$arch" in x86_64|aarch64|arm64) ;; *) echo "unsupported arch $arch" >&2; exit 1 ;; esac
      tmp=$(mktemp -d)
      curl -fsSL "https://github.com/jedisct1/minisign/releases/download/0.12/minisign-0.12-linux.tar.gz" -o "$tmp/m.tgz"
      echo "9a599b48ba6eb7b1e80f12f36b94ceca7c00b7a5173c95c3efc88d9822957e73  $tmp/m.tgz" | sha256sum -c -
      tar -xzf "$tmp/m.tgz" -C "$tmp" 2>/dev/null
      install -m0755 "$tmp/minisign-linux/$arch/minisign" /usr/local/bin/minisign
      rm -rf "$tmp"
      ;;
    *)
      apt-get install -y "$1"
      ;;
  esac
}

if [ -f "$HOST_TOOLSET" ]; then
  apt-get update -qq
  while read -r tool; do
    tool="${tool%%#*}"; tool="${tool// /}"
    [ -n "$tool" ] || continue
    command -v "$tool" >/dev/null 2>&1 || { echo "toolset: installing $tool"; install_tool "$tool"; }
  done < "$HOST_TOOLSET"
fi

mkdir -p "$AGENT_DIR"
cd "$AGENT_DIR"
if [ ! -x bin/Runner.Listener ]; then
  curl -fsSL "https://github.com/actions/runner/releases/download/v${RUNNER_VERSION}/actions-runner-linux-x64-${RUNNER_VERSION}.tar.gz" | tar -xz
fi
./config.sh --unattended --replace \
  --url "https://github.com/${REPO}" \
  --token "$RUNNER_TOKEN" \
  --name "ci-runner-${CT}-$(hostname -s | tr -c 'a-z0-9-' '-')" \
  --labels "$LABELS" \
  --work _work
./svc.sh install && ./svc.sh start
echo "installed ${REPO} agent on lxc-${CT} with labels: ${LABELS}"

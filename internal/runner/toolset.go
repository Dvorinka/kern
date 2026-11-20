package runner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

// toolset.go is the fleet's knowledge of tools a bare apt name cannot
// provide. Two consumers:
//
//   - diagnosis.go remediation: a missing_binary on `gh` or `minisign` gets
//     a tarball install plan instead of an apt-get that would 404.
//   - the org `toolset` drift check: a host executor diffs the declared
//     list against its PATH every few heartbeats and reports the gap; with
//     self_heal=auto it installs from the same plans.
//
// toolHints covers tools whose install needs a toolchain we cannot assume
// (cargo, corepack) — the diagnosis names the command instead of offering a
// doomed apt-get.
//
// Every Fix command is a fixed template — nothing is interpolated from job
// logs. Platform and arch are resolved inside the shell (`uname`) so the
// same plan works in a job container and on a host executor. Windows plans
// run under Git Bash and install into $KERN_TOOL_BIN — the runner pins it
// to a writable PATH dir via hostShEnv because the MSYS PATH contains
// entries (~/bin, /usr/bin) that exec.LookPath cannot see.

// toolPlan is one curated install: Pkg names what provides the tool for
// display; Fix is the ordered shell plan.
type toolPlan struct {
	Pkg string
	Fix []string
}

// ghInstall downloads a PINNED gh release for the local platform, verifies
// the archive against a checked-in sha256, then installs to a PATH-visible
// bin dir. self_heal=auto runs these on host executors — an unverified
// mutable "latest" tarball would be remote code execution on the fleet, so
// the digests are baked in here and bumped deliberately with the version.
const ghInstall = `os=$(uname -s) && case "$os" in ` +
	`Linux) arch=$(uname -m) && case "$arch" in ` +
	`x86_64) a=amd64; s=98cb101ad87dbbd3f6b2f2bfcbd43197ccd07643140121d8a9cfaa91eadbc24e;; ` +
	`aarch64|arm64) a=arm64; s=cb34cefc6f4e77b90731374a2b737516bb8f3033c6ca46aa949e5344f68069e3;; ` +
	`*) echo "unsupported arch $arch" >&2; exit 1;; esac` +
	` && tmp=$(mktemp -d) && curl -fsSL "https://github.com/cli/cli/releases/download/v2.104.0/gh_2.104.0_linux_$a.tar.gz" -o "$tmp/gh.tgz"` +
	` && echo "$s  $tmp/gh.tgz" | sha256sum -c - && tar -xzf "$tmp/gh.tgz" -C "$tmp"` +
	` && install -m0755 "$tmp"/gh_*/bin/gh /usr/local/bin/gh && rm -rf "$tmp" && gh version;; ` +
	`MINGW*|MSYS*|CYGWIN*|Windows*) [ -n "${KERN_TOOL_BIN:-}" ] || { echo "no writable PATH dir for tool install" >&2; exit 1; };` +
	` arch=$(uname -m) && case "$arch" in ` +
	`x86_64|amd64) a=amd64; s=08c06d10482f12c478018cd259f64acbd5f625712cadf331aad19ba973c879e5;; ` +
	`aarch64|arm64) a=arm64; s=d56fbf5b9ad32e5985e3e7e1d8f4d28ae2994618986cd2cba9b50ac710dffe01;; ` +
	`*) echo "unsupported arch $arch" >&2; exit 1;; esac` +
	` && dest=$(cygpath -u "$KERN_TOOL_BIN") && tmp=$(mktemp -d)` +
	` && curl -fsSL "https://github.com/cli/cli/releases/download/v2.104.0/gh_2.104.0_windows_$a.zip" -o "$tmp/gh.zip"` +
	` && echo "$s  $tmp/gh.zip" | sha256sum -c - && unzip -o "$tmp/gh.zip" -d "$tmp"` +
	` && install -m0755 "$tmp/bin/gh.exe" "$dest/gh.exe" && rm -rf "$tmp" && "$dest/gh.exe" version;; ` +
	`Darwin) arch=$(uname -m) && case "$arch" in ` +
	`arm64) a=arm64; s=23b12706219b9892e44236f988444a24adcf29284e16b35e03ba5b863fb6eb8e;; ` +
	`x86_64) a=amd64; s=a8c2a708dbae89dc7c05ba7b0beafc95bd8d09ab4a3505467b77d32ebf364dc6;; ` +
	`*) echo "unsupported arch $arch" >&2; exit 1;; esac` +
	` && dest=/usr/local/bin && { [ -w "$dest" ] || dest=/opt/homebrew/bin; } && [ -w "$dest" ] || { echo "no writable PATH dir for tool install" >&2; exit 1; }` +
	` && case ":$PATH:" in *":$dest:"*) ;; *) echo "$dest not on PATH" >&2; exit 1;; esac` +
	` && tmp=$(mktemp -d) && curl -fsSL "https://github.com/cli/cli/releases/download/v2.104.0/gh_2.104.0_macOS_$a.zip" -o "$tmp/gh.zip"` +
	` && echo "$s  $tmp/gh.zip" | shasum -a 256 -c - && unzip -o "$tmp/gh.zip" -d "$tmp"` +
	` && install -m0755 "$tmp"/gh_*/bin/gh "$dest/gh" && rm -rf "$tmp" && "$dest/gh" version;; ` +
	`*) echo "unsupported os $os" >&2; exit 1;; esac`

const minisignInstall = `os=$(uname -s) && case "$os" in ` +
	`Linux) arch=$(uname -m) && case "$arch" in x86_64|aarch64|arm64) a=$arch;; *) echo "unsupported arch $arch" >&2; exit 1;; esac` +
	` && tmp=$(mktemp -d) && curl -fsSL "https://github.com/jedisct1/minisign/releases/download/0.12/minisign-0.12-linux.tar.gz" -o "$tmp/m.tgz"` +
	` && echo "9a599b48ba6eb7b1e80f12f36b94ceca7c00b7a5173c95c3efc88d9822957e73  $tmp/m.tgz" | sha256sum -c -` +
	` && tar -xzf "$tmp/m.tgz" -C "$tmp" 2>/dev/null && install -m0755 "$tmp/minisign-linux/$a/minisign" /usr/local/bin/minisign && rm -rf "$tmp" && minisign -v;; ` +
	`MINGW*|MSYS*|CYGWIN*|Windows*) [ -n "${KERN_TOOL_BIN:-}" ] || { echo "no writable PATH dir for tool install" >&2; exit 1; };` +
	` arch=$(uname -m) && case "$arch" in x86_64|amd64) a=x86_64;; aarch64|arm64) a=aarch64;; *) echo "unsupported arch $arch" >&2; exit 1;; esac` +
	` && dest=$(cygpath -u "$KERN_TOOL_BIN") && tmp=$(mktemp -d)` +
	` && curl -fsSL "https://github.com/jedisct1/minisign/releases/download/0.12/minisign-0.12-win64.zip" -o "$tmp/m.zip"` +
	` && echo "37b600344e20c19314b2e82813db2bfdcc408b77b876f7727889dbd46d539479  $tmp/m.zip" | sha256sum -c -` +
	` && unzip -o "$tmp/m.zip" -d "$tmp" && install -m0755 "$tmp/minisign-win64/$a/minisign.exe" "$dest/minisign.exe" && rm -rf "$tmp" && "$dest/minisign.exe" -v;; ` +
	// Upstream ships no checksum-pinned Intel macOS binary (macos.zip is
	// arm64-only); Homebrew's formula carries its own pinned sha256.
	`Darwin) if command -v brew >/dev/null 2>&1; then brew install minisign && minisign -v;` +
	` else echo "no minisign plan for macOS without Homebrew" >&2; exit 1; fi;; ` +
	`*) echo "unsupported os $os" >&2; exit 1;; esac`

// The security scanners the fleet's security.yml workflow uses. Same
// contract as gh/minisign: pinned release, per-arch sha256 baked in,
// PATH-visible install target. Trivy publishes no Windows ARM64 asset,
// so that arch reports unsupported rather than installing nothing.
const trivyInstall = `os=$(uname -s) && case "$os" in ` +
	`Linux) arch=$(uname -m) && case "$arch" in ` +
	`x86_64) f=trivy_0.76.2_Linux-64bit.tar.gz; s=bc0fc726e017b8927a62b2d3531d7a84ee01769daf5c99ed866dced54089fdbf;; ` +
	`aarch64|arm64) f=trivy_0.76.2_Linux-ARM64.tar.gz; s=ab9d51f86e6a5fb936f605faff6b593ac51bc378fff4d3e8b9d58358b9c367c3;; ` +
	`*) echo "unsupported arch $arch" >&2; exit 1;; esac` +
	` && tmp=$(mktemp -d) && curl -fsSL "https://github.com/aquasecurity/trivy/releases/download/v0.76.2/$f" -o "$tmp/$f"` +
	` && echo "$s  $tmp/$f" | sha256sum -c - && tar -xzf "$tmp/$f" -C "$tmp"` +
	` && install -m0755 "$tmp/trivy" /usr/local/bin/trivy && rm -rf "$tmp" && trivy --version;; ` +
	`MINGW*|MSYS*|CYGWIN*|Windows*) [ -n "${KERN_TOOL_BIN:-}" ] || { echo "no writable PATH dir for tool install" >&2; exit 1; };` +
	` arch=$(uname -m) && case "$arch" in ` +
	`x86_64|amd64) f=trivy_0.76.2_windows-64bit.zip; s=691f737a7e5566a673c3a74dab9160552ec32101e953b659e5c77443d16dddb6;; ` +
	`*) echo "unsupported arch $arch" >&2; exit 1;; esac` +
	` && dest=$(cygpath -u "$KERN_TOOL_BIN") && tmp=$(mktemp -d)` +
	` && curl -fsSL "https://github.com/aquasecurity/trivy/releases/download/v0.76.2/$f" -o "$tmp/$f"` +
	` && echo "$s  $tmp/$f" | sha256sum -c - && unzip -o "$tmp/$f" -d "$tmp"` +
	` && install -m0755 "$tmp/trivy.exe" "$dest/trivy.exe" && rm -rf "$tmp" && "$dest/trivy.exe" --version;; ` +
	`Darwin) arch=$(uname -m) && case "$arch" in ` +
	`arm64) f=trivy_0.76.2_macOS-ARM64.tar.gz; s=ae7ea358326960406b703c77b2ac490190797d80b306b5b50c2a27221da0a5b8;; ` +
	`x86_64) f=trivy_0.76.2_macOS-64bit.tar.gz; s=e08bb9ea449ecf66c6bfb03cbb8697c503ff33d35fc585c9747e3eaeb7ed4c7b;; ` +
	`*) echo "unsupported arch $arch" >&2; exit 1;; esac` +
	` && dest=/usr/local/bin && { [ -w "$dest" ] || dest=/opt/homebrew/bin; } && [ -w "$dest" ] || { echo "no writable PATH dir for tool install" >&2; exit 1; }` +
	` && case ":$PATH:" in *":$dest:"*) ;; *) echo "$dest not on PATH" >&2; exit 1;; esac` +
	` && tmp=$(mktemp -d) && curl -fsSL "https://github.com/aquasecurity/trivy/releases/download/v0.76.2/$f" -o "$tmp/$f"` +
	` && echo "$s  $tmp/$f" | shasum -a 256 -c - && tar -xzf "$tmp/$f" -C "$tmp"` +
	` && install -m0755 "$tmp/trivy" "$dest/trivy" && rm -rf "$tmp" && "$dest/trivy" --version;; ` +
	`*) echo "unsupported os $os" >&2; exit 1;; esac`

const osvScannerInstall = `os=$(uname -s) && case "$os" in ` +
	`Linux) arch=$(uname -m) && case "$arch" in ` +
	`x86_64) f=osv-scanner_linux_amd64; s=fb7dab19a8bd9bbe5c3189f09d4b7ee1820c9553a23bac202648a56efd2f6ee8;; ` +
	`aarch64|arm64) f=osv-scanner_linux_arm64; s=983d22fbad9724514e3fcc6acc83b5fa64c5ffa6b0d9f510028f22c1b223ff81;; ` +
	`*) echo "unsupported arch $arch" >&2; exit 1;; esac` +
	` && tmp=$(mktemp -d) && curl -fsSL "https://github.com/google/osv-scanner/releases/download/v2.7.1/$f" -o "$tmp/osv-scanner"` +
	` && echo "$s  $tmp/osv-scanner" | sha256sum -c -` +
	` && install -m0755 "$tmp/osv-scanner" /usr/local/bin/osv-scanner && rm -rf "$tmp" && osv-scanner --version;; ` +
	`MINGW*|MSYS*|CYGWIN*|Windows*) [ -n "${KERN_TOOL_BIN:-}" ] || { echo "no writable PATH dir for tool install" >&2; exit 1; };` +
	` arch=$(uname -m) && case "$arch" in ` +
	`x86_64|amd64) f=osv-scanner_windows_amd64.exe; s=7121dcf420aefbbf89a7f0050c4d3094e079ff93afd334dfc96f16b02ec85800;; ` +
	`aarch64|arm64) f=osv-scanner_windows_arm64.exe; s=5f3998b55c9268f8468502bbcb60645038fd5b145ee5e685e6ae2a32e923afc7;; ` +
	`*) echo "unsupported arch $arch" >&2; exit 1;; esac` +
	` && dest=$(cygpath -u "$KERN_TOOL_BIN") && tmp=$(mktemp -d)` +
	` && curl -fsSL "https://github.com/google/osv-scanner/releases/download/v2.7.1/$f" -o "$tmp/osv-scanner.exe"` +
	` && echo "$s  $tmp/osv-scanner.exe" | sha256sum -c -` +
	` && install -m0755 "$tmp/osv-scanner.exe" "$dest/osv-scanner.exe" && rm -rf "$tmp" && "$dest/osv-scanner.exe" --version;; ` +
	`Darwin) arch=$(uname -m) && case "$arch" in ` +
	`arm64) f=osv-scanner_darwin_arm64; s=b8420d5485ba792626315f1bf9a5a2046e971d38eea75bf9090c38852dbc3b1b;; ` +
	`x86_64) f=osv-scanner_darwin_amd64; s=b22c02877a811c8f78956e8643efa554932b7378f590d13f8e97d4e9448a29f3;; ` +
	`*) echo "unsupported arch $arch" >&2; exit 1;; esac` +
	` && dest=/usr/local/bin && { [ -w "$dest" ] || dest=/opt/homebrew/bin; } && [ -w "$dest" ] || { echo "no writable PATH dir for tool install" >&2; exit 1; }` +
	` && case ":$PATH:" in *":$dest:"*) ;; *) echo "$dest not on PATH" >&2; exit 1;; esac` +
	` && tmp=$(mktemp -d) && curl -fsSL "https://github.com/google/osv-scanner/releases/download/v2.7.1/$f" -o "$tmp/osv-scanner"` +
	` && echo "$s  $tmp/osv-scanner" | shasum -a 256 -c -` +
	` && install -m0755 "$tmp/osv-scanner" "$dest/osv-scanner" && rm -rf "$tmp" && "$dest/osv-scanner" --version;; ` +
	`*) echo "unsupported os $os" >&2; exit 1;; esac`

const gitleaksInstall = `os=$(uname -s) && case "$os" in ` +
	`Linux) arch=$(uname -m) && case "$arch" in ` +
	`x86_64) f=gitleaks_8.30.1_linux_x64.tar.gz; s=551f6fc83ea457d62a0d98237cbad105af8d557003051f41f3e7ca7b3f2470eb;; ` +
	`aarch64|arm64) f=gitleaks_8.30.1_linux_arm64.tar.gz; s=e4a487ee7ccd7d3a7f7ec08657610aa3606637dab924210b3aee62570fb4b080;; ` +
	`*) echo "unsupported arch $arch" >&2; exit 1;; esac` +
	` && tmp=$(mktemp -d) && curl -fsSL "https://github.com/gitleaks/gitleaks/releases/download/v8.30.1/$f" -o "$tmp/$f"` +
	` && echo "$s  $tmp/$f" | sha256sum -c - && tar -xzf "$tmp/$f" -C "$tmp"` +
	` && install -m0755 "$tmp/gitleaks" /usr/local/bin/gitleaks && rm -rf "$tmp" && gitleaks version;; ` +
	`MINGW*|MSYS*|CYGWIN*|Windows*) [ -n "${KERN_TOOL_BIN:-}" ] || { echo "no writable PATH dir for tool install" >&2; exit 1; };` +
	` arch=$(uname -m) && case "$arch" in ` +
	`x86_64|amd64) f=gitleaks_8.30.1_windows_x64.zip; s=d29144deff3a68aa93ced33dddf84b7fdc26070add4aa0f4513094c8332afc4e;; ` +
	`aarch64|arm64) f=gitleaks_8.30.1_windows_arm64.zip; s=b95f5e4f5c425cedca7ee203d9afd29597e692c4924a12ed42f970537c72cc0f;; ` +
	`*) echo "unsupported arch $arch" >&2; exit 1;; esac` +
	` && dest=$(cygpath -u "$KERN_TOOL_BIN") && tmp=$(mktemp -d)` +
	` && curl -fsSL "https://github.com/gitleaks/gitleaks/releases/download/v8.30.1/$f" -o "$tmp/$f"` +
	` && echo "$s  $tmp/$f" | sha256sum -c - && unzip -o "$tmp/$f" -d "$tmp"` +
	` && install -m0755 "$tmp/gitleaks.exe" "$dest/gitleaks.exe" && rm -rf "$tmp" && "$dest/gitleaks.exe" version;; ` +
	`Darwin) arch=$(uname -m) && case "$arch" in ` +
	`arm64) f=gitleaks_8.30.1_darwin_arm64.tar.gz; s=b40ab0ae55c505963e365f271a8d3846efbc170aa17f2607f13df610a9aeb6a5;; ` +
	`x86_64) f=gitleaks_8.30.1_darwin_x64.tar.gz; s=dfe101a4db2255fc85120ac7f3d25e4342c3c20cf749f2c20a18081af1952709;; ` +
	`*) echo "unsupported arch $arch" >&2; exit 1;; esac` +
	` && dest=/usr/local/bin && { [ -w "$dest" ] || dest=/opt/homebrew/bin; } && [ -w "$dest" ] || { echo "no writable PATH dir for tool install" >&2; exit 1; }` +
	` && case ":$PATH:" in *":$dest:"*) ;; *) echo "$dest not on PATH" >&2; exit 1;; esac` +
	` && tmp=$(mktemp -d) && curl -fsSL "https://github.com/gitleaks/gitleaks/releases/download/v8.30.1/$f" -o "$tmp/$f"` +
	` && echo "$s  $tmp/$f" | shasum -a 256 -c - && tar -xzf "$tmp/$f" -C "$tmp"` +
	` && install -m0755 "$tmp/gitleaks" "$dest/gitleaks" && rm -rf "$tmp" && "$dest/gitleaks" version;; ` +
	`*) echo "unsupported os $os" >&2; exit 1;; esac`

var toolInstalls = map[string]toolPlan{
	"gh":          {Pkg: "cli/cli release tarball", Fix: []string{ghInstall}},
	"minisign":    {Pkg: "jedisct1/minisign release tarball", Fix: []string{minisignInstall}},
	"trivy":       {Pkg: "aquasecurity/trivy release tarball", Fix: []string{trivyInstall}},
	"osv-scanner": {Pkg: "google/osv-scanner release binary", Fix: []string{osvScannerInstall}},
	"gitleaks":    {Pkg: "gitleaks/gitleaks release tarball", Fix: []string{gitleaksInstall}},
}

// toolHints names tools that cannot self-install from a fixed template —
// they need a toolchain the job or host may not have. The diagnosis points
// at the install command rather than a wrong apt package.
var toolHints = map[string]string{
	"tauri-driver": "install it in a prior step via `cargo install tauri-driver` or use an image that ships it",
	"pnpm":         "install it in a prior step via `corepack enable && corepack prepare pnpm@latest --activate` or use an image that ships it",
}

// toolsetNameRe gates manifest entries the way pkgNameRe gates diagnoses:
// the toolset becomes shell input through install plans, so only sane
// command names are allowed.
var toolsetNameRe = regexp.MustCompile(`^[a-zA-Z0-9._+-]{1,64}$`)

// missingTools diffs a declared toolset against the host PATH. exec.LookPath
// resolves the runner's own environment — correct on the host executor,
// where runner process and jobs share the filesystem.
func missingTools(declared []string) []string {
	var missing []string
	for _, name := range declared {
		if !toolsetNameRe.MatchString(name) {
			continue
		}
		if _, err := exec.LookPath(name); err != nil {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing
}

// installHostTool runs the curated plan for one tool on the host. Tools
// without a plan (apt-named entries) get the generic package-manager
// install template — the same one step remediation uses.
func installHostTool(ctx context.Context, name string) error {
	fix, ok := toolInstalls[name]
	if !ok {
		fix = toolPlan{Pkg: name, Fix: installCommands(name)}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	for _, cmdline := range fix.Fix {
		cmd := exec.CommandContext(ctx, "sh", "-c", cmdline)
		cmd.Env = hostShEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%s install failed: %v: %s", name, err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// hostShEnv is the environment for host-side `sh -c` plan execution. On
// Windows it pins KERN_TOOL_BIN to a PATH-visible writable directory —
// Git Bash prepends MSYS-only dirs (~/bin, /usr/bin, /mingw64/bin) that
// exec.LookPath cannot resolve, so the shell cannot pick a safe install
// target itself.
func hostShEnv() []string {
	env := os.Environ()
	if runtime.GOOS == "windows" {
		if dir := winToolBin(); dir != "" {
			env = append(env, "KERN_TOOL_BIN="+dir)
		}
	}
	return env
}

// winToolBin returns the first writable, non-system directory on the
// runner's PATH — the same PATH exec.LookPath resolves tool names against.
func winToolBin() string {
	return pickToolBin(os.Getenv("PATH"))
}

// pickToolBin scans a PATH list for the tool install target. Only absolute
// directories qualify: Git Bash PATHs carry MSYS mount points (/usr/bin,
// ~/bin, /mingw64/bin) that Windows file APIs cannot address, and a
// relative entry would resolve against an arbitrary cwd.
func pickToolBin(pathList string) string {
	for _, dir := range filepath.SplitList(pathList) {
		if !filepath.IsAbs(dir) || isSystemBinDir(dir) || !isWritableDir(dir) {
			continue
		}
		return dir
	}
	return ""
}

// isSystemBinDir reports whether dir sits under a Windows system root —
// installing fleet tools into Program Files or Windows dirs is wrong even
// when the runner token could write there.
func isSystemBinDir(dir string) bool {
	parts := strings.Split(strings.ToLower(filepath.Clean(strings.ReplaceAll(dir, "/", `\`))), `\`)
	if len(parts) < 2 {
		return false
	}
	switch parts[1] {
	case "windows", "program files", "program files (x86)", "programdata":
		return true
	}
	return false
}

// isWritableDir reports whether dir exists and accepts new files — an
// actual write probe, since PATH membership says nothing about ACLs.
func isWritableDir(dir string) bool {
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		return false
	}
	f, err := os.CreateTemp(dir, ".kern-probe-")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}

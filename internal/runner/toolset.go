package runner

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
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
// logs. Arch is resolved inside the shell (`uname -m`) so the same plan
// works in a job container and on a host executor.

// toolPlan is one curated install: Pkg names what provides the tool for
// display; Fix is the ordered shell plan.
type toolPlan struct {
	Pkg string
	Fix []string
}

// latestGH downloads the newest gh release tarball for the local arch and
// drops the binary into /usr/local/bin. The tag is resolved from the
// releases/latest redirect so the plan needs no version interpolation.
const ghInstall = `tag=$(curl -fsSIL -o /dev/null -w '%{url_effective}' https://github.com/cli/cli/releases/latest | sed 's/.*tag\///')` +
	` && arch=$(uname -m) && case "$arch" in x86_64) a=amd64;; aarch64|arm64) a=arm64;; *) echo "unsupported arch $arch" >&2; exit 1;; esac` +
	` && tmp=$(mktemp -d) && curl -fsSL "https://github.com/cli/cli/releases/download/$tag/gh_${tag#v}_linux_$a.tar.gz" -o "$tmp/gh.tgz"` +
	` && tar -xzf "$tmp/gh.tgz" -C "$tmp" && install -m0755 "$tmp"/gh_*/bin/gh /usr/local/bin/gh && rm -rf "$tmp" && gh version`

const minisignInstall = `tag=$(curl -fsSIL -o /dev/null -w '%{url_effective}' https://github.com/jedisct1/minisign/releases/latest | sed 's/.*tag\///')` +
	` && arch=$(uname -m) && case "$arch" in x86_64) a=x86_64;; aarch64|arm64) a=aarch64;; *) echo "unsupported arch $arch" >&2; exit 1;; esac` +
	` && tmp=$(mktemp -d) && curl -fsSL "https://github.com/jedisct1/minisign/releases/download/$tag/minisign-${tag#v}-linux.tar.gz" -o "$tmp/m.tgz"` +
	` && tar -xzf "$tmp/m.tgz" -C "$tmp" 2>/dev/null && install -m0755 "$tmp/minisign-linux/$a/minisign" /usr/local/bin/minisign && rm -rf "$tmp" && minisign -v`

var toolInstalls = map[string]toolPlan{
	"gh":       {Pkg: "cli/cli release tarball", Fix: []string{ghInstall}},
	"minisign": {Pkg: "jedisct1/minisign release tarball", Fix: []string{minisignInstall}},
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
		if out, err := exec.CommandContext(ctx, "sh", "-c", cmdline).CombinedOutput(); err != nil {
			return fmt.Errorf("%s install failed: %v: %s", name, err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

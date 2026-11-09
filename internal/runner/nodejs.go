package runner

// JavaScript action runtime. GitHub runs `uses:` steps whose action.yml says
// runs.using: node* on a Node distribution bundled with the runner; KERN does
// the same by downloading a pinned Node release into <work-root>/_node and
// mounting it read-only at /kern/node. The action runs inside the job
// container via docker exec, like any other step: same read-only root,
// dropped capabilities, INPUT_* and GITHUB_* env contract.

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/dvorinka/kern/internal/actions"
	"github.com/dvorinka/kern/internal/expressions"
)

const (
	// nodeDistBase is the Node release archive origin; SHASUMS256.txt beside
	// each tarball proves download integrity.
	nodeDistBase        = "https://nodejs.org/dist"
	containerNodeRoot   = "/kern/node" // _node is mounted here; children may be re-provisioned mid-job
	nodeDownloadTimeout = 5 * time.Minute
)

// nodeReleases maps action runtimes to pinned Node distributions, provisioned
// lazily under <work-root>/_node/<release>. GitHub retargets node12/node16
// onto node20; node24 gets its own major — actions declaring node24 (type
// stripping, --experimental-strip-types, newer APIs) must not run on v20.
var nodeReleases = map[string]string{
	"node12": "v20.18.1",
	"node16": "v20.18.1",
	"node20": "v20.18.1",
	"node24": "v24.22.2",
}

// nodeReleaseFor resolves an action runtime name to its pinned release.
// Unknown runtimes fall back to the node20 pin.
func nodeReleaseFor(using string) string {
	if r, ok := nodeReleases[using]; ok {
		return r
	}
	return nodeReleases["node20"]
}

// nodeHostDir is the runner-persistent location of the unpacked distribution
// for one runtime. The container mounts its parent _node, so a re-provisioned
// child directory stays visible inside running job containers.
func (e *executor) nodeHostDir(using string) string {
	return filepath.Join(e.cfg.WorkRoot, "_node", nodeReleaseFor(using))
}

// containerNodeBin is the node binary as executed code sees it: /kern/node
// inside job containers, the host path on the host executor. The Windows
// dist unpacks node.exe at the archive root (no bin/ dir).
func (e *executor) nodeBin(using string) string {
	if e.host {
		if runtime.GOOS == "windows" {
			return filepath.Join(e.nodeHostDir(using), "node.exe")
		}
		return filepath.Join(e.nodeHostDir(using), "bin", "node")
	}
	return containerNodeRoot + "/" + nodeReleaseFor(using) + "/bin/node"
}

// nodeBinDir is that binary's directory. Actions start with an absolute
// node path, but scripts they spawn (pnpm's preinstall) call `node` on PATH.
func (e *executor) nodeBinDir(using string) string {
	return filepath.Dir(e.nodeBin(using))
}

// nodeArchive maps the runner platform to the dist archive name. Windows
// ships a .zip (node.exe at the root); unix ships a .tar.gz with bin/node.
func nodeArchive(release string) (string, error) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		return "", fmt.Errorf("JavaScript actions need a linux, darwin or windows runner host (got %s)", runtime.GOOS)
	}
	var arch string
	switch runtime.GOARCH {
	case "amd64":
		arch = "x64"
	case "arm64":
		arch = "arm64"
	default:
		return "", fmt.Errorf("JavaScript actions are unsupported on %s runners", runtime.GOARCH)
	}
	if runtime.GOOS == "windows" {
		return fmt.Sprintf("node-%s-win-%s.zip", release, arch), nil
	}
	return fmt.Sprintf("node-%s-%s-%s.tar.gz", release, runtime.GOOS, arch), nil
}

// ensureNode provisions the Node distribution for one runtime once per
// runner host. A <dir>.complete marker gates readers, so a crashed download
// never leaves a half-unpacked runtime for the next job.
var nodeProvisionMu sync.Mutex

func (e *executor) ensureNode(ctx context.Context, using string) error {
	nodeProvisionMu.Lock()
	defer nodeProvisionMu.Unlock()
	release := nodeReleaseFor(using)
	dir := e.nodeHostDir(using)
	if _, err := os.Stat(dir + ".complete"); err == nil {
		return nil
	}
	archive, err := nodeArchive(release)
	if err != nil {
		return err
	}
	tmp := dir + fmt.Sprintf(".tmp-%d", os.Getpid())
	_ = os.RemoveAll(tmp)
	defer os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return err
	}
	pkg := filepath.Join(tmp, archive)
	if err := download(ctx, nodeDistBase+"/"+release+"/"+archive, pkg); err != nil {
		return fmt.Errorf("download node %s: %w", release, err)
	}
	if err := verifyDistSum(ctx, release, archive, pkg); err != nil {
		return err
	}
	root := strings.TrimSuffix(strings.TrimSuffix(archive, ".tar.gz"), ".zip")
	if strings.HasSuffix(archive, ".zip") {
		if err := unzip(pkg, tmp); err != nil {
			return fmt.Errorf("unpack node: %w", err)
		}
	} else if err := untarGz(pkg, tmp); err != nil {
		return fmt.Errorf("unpack node: %w", err)
	}
	_ = os.RemoveAll(dir)
	if err := os.Rename(filepath.Join(tmp, root), dir); err != nil {
		return err
	}
	return os.WriteFile(dir+".complete", []byte(release+"\n"), 0o644)
}

// verifyDistSum checks the tarball against the SHASUMS256.txt published with
// the release. The checksum file is fetched over https from the same origin;
// it proves download integrity, not publisher trust.
func verifyDistSum(ctx context.Context, release, archive, tgz string) error {
	sums := filepath.Join(filepath.Dir(tgz), "SHASUMS256.txt")
	if err := download(ctx, nodeDistBase+"/"+release+"/SHASUMS256.txt", sums); err != nil {
		return fmt.Errorf("download node checksums: %w", err)
	}
	data, err := os.ReadFile(sums)
	if err != nil {
		return err
	}
	var want string
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == archive {
			want = fields[0]
		}
	}
	if want == "" {
		return fmt.Errorf("no checksum for %s in SHASUMS256.txt", archive)
	}
	fh, err := os.Open(tgz)
	if err != nil {
		return err
	}
	defer fh.Close()
	h := sha256.New()
	if _, err := io.Copy(h, fh); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("node tarball checksum mismatch: got %s want %s", got, want)
	}
	return nil
}

// unzip extracts a .zip archive into dest. Entries are confined to dest —
// a member with ../ or an absolute path is skipped rather than allowed to
// write outside the provisioning dir.
func unzip(archive, dest string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, f := range zr.File {
		name := filepath.Clean(f.Name)
		if filepath.IsAbs(name) || strings.HasPrefix(name, "..") {
			continue
		}
		out := filepath.Join(dest, name)
		if !strings.HasPrefix(out, filepath.Clean(dest)+string(os.PathSeparator)) {
			continue
		}
		if f.FileInfo().IsDir() {
			_ = os.MkdirAll(out, 0o755)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		w, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
		if err != nil {
			rc.Close()
			return err
		}
		_, copyErr := io.Copy(w, rc)
		w.Close()
		rc.Close()
		if copyErr != nil {
			return copyErr
		}
	}
	return nil
}

// download fetches url to path with a bounded context.
func download(ctx context.Context, url, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: nodeDownloadTimeout}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	fh, err := os.Create(path)
	if err != nil {
		return err
	}
	defer fh.Close()
	_, err = io.Copy(fh, io.LimitReader(resp.Body, 300<<20))
	return err
}

// inputEnv builds the INPUT_* variables GitHub exports for a JavaScript
// action: every `with` key (uppercased, spaces as underscores) plus declared
// inputs resolved to their default when not supplied.
func inputEnv(meta *actions.Meta, with map[string]any, ctx map[string]any) map[string]string {
	env := map[string]string{}
	for name, in := range meta.Inputs {
		v := in.Default
		if strings.Contains(v, "${{") {
			if r, err := expressions.Interpolate(v, ctx); err == nil {
				v = r
			}
		}
		for k, w := range with {
			if strings.EqualFold(k, name) {
				v = envString(w)
			}
		}
		env[inputKey(name)] = v
	}
	for k, w := range with {
		env[inputKey(k)] = envString(w)
	}
	return env
}

func inputKey(name string) string {
	return "INPUT_" + strings.ToUpper(strings.ReplaceAll(name, " ", "_"))
}

// nodeActionEnv builds the env shared by an action's pre/main/post scripts:
// step env, GITHUB_ACTION_PATH, INPUT_* and any saved STATE_* values.
func (e *executor) nodeActionEnv(step StepSpec, meta *actions.Meta, actionPath string, state map[string]string) map[string]string {
	env := map[string]string{}
	for k, v := range step.Env {
		env[k] = v
	}
	env["GITHUB_ACTION_PATH"] = actionPath
	for k, v := range inputEnv(meta, step.With, e.runnerContext()) {
		env[k] = v
	}
	for k, v := range state {
		env["STATE_"+k] = v
	}
	return env
}

// runNodeScript executes one JavaScript entrypoint (pre/main/post) of a node
// action inside the job container on the bundled Node runtime.
func (e *executor) runNodeScript(ctx context.Context, step StepSpec, meta *actions.Meta, actionPath, script string, state map[string]string) (string, int) {
	using := meta.Runs.Using
	if err := e.ensureNode(ctx, using); err != nil {
		e.logStep(step, "KERN: node runtime: "+err.Error())
		return "failure", 1
	}
	e.enc.persistPaths([]string{e.nodeBinDir(using)})
	sub := StepSpec{
		Number:         step.Number,
		ID:             step.ID,
		Name:           step.Name,
		Run:            shellQuote(e.nodeBin(using)) + " " + shellQuote(actionPath+"/"+script),
		Shell:          e.shell,
		Env:            e.nodeActionEnv(step, meta, actionPath, state),
		TimeoutSeconds: step.TimeoutSeconds,
	}
	outcome, code, err := e.runStepBody(ctx, sub)
	if err != nil {
		e.logStep(step, "KERN: "+err.Error())
	}
	return outcome, code
}

// runNodeAction executes a JavaScript action's main entrypoint inside the job
// container. The post hook arms before main runs; GITHUB_STATE is drained
// into it afterwards so the post phase sees the saved STATE_* values.
func (e *executor) runNodeAction(ctx context.Context, step StepSpec, meta *actions.Meta, actionPath string) (string, int) {
	main := strings.TrimSpace(meta.Runs.Main)
	if main == "" {
		e.logStep(step, "KERN: "+step.Uses+" declares no runs.main")
		return "failure", 1
	}
	if meta.Runs.Using != "node20" {
		e.logStep(step, fmt.Sprintf("KERN: runs.using %s executes on the bundled %s runtime", meta.Runs.Using, nodeReleaseFor(meta.Runs.Using)))
	}
	if !e.runNestedPre(ctx, step, meta, actionPath, "") {
		return "failure", 1
	}
	h := e.armPostHook(step, meta, actionPath, "")
	outcome, code := e.runNodeScript(ctx, step, meta, actionPath, main, nil)
	if h != nil {
		mergeState(h, e.drainState())
	}
	return outcome, code
}

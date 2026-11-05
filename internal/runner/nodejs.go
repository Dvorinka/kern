package runner

// JavaScript action runtime. GitHub runs `uses:` steps whose action.yml says
// runs.using: node* on a Node distribution bundled with the runner; KERN does
// the same by downloading a pinned Node release into <work-root>/_node and
// mounting it read-only at /kern/node. The action runs inside the job
// container via docker exec, like any other step: same read-only root,
// dropped capabilities, INPUT_* and GITHUB_* env contract.

import (
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
)

const (
	// nodeRelease is the pinned Node distribution every node* action runtime
	// resolves to. GitHub likewise retargets node12/node16 onto node20.
	nodeRelease         = "v20.18.1"
	nodeDistBase        = "https://nodejs.org/dist"
	containerNodeRoot   = "/kern/node" // _node is mounted here; children may be re-provisioned mid-job
	nodeDownloadTimeout = 5 * time.Minute
)

// nodeHostDir is the runner-persistent location of the unpacked distribution.
// The container mounts its parent _node, so a re-provisioned child directory
// stays visible inside running job containers.
func (e *executor) nodeHostDir() string {
	return filepath.Join(e.cfg.WorkRoot, "_node", nodeRelease)
}

// containerNodeBin is the node binary as the job container sees it.
func containerNodeBin() string {
	return containerNodeRoot + "/" + nodeRelease + "/bin/node"
}

// nodeArchive maps the host platform to the dist tarball name.
func nodeArchive() (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("JavaScript actions need a linux runner host (got %s)", runtime.GOOS)
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
	return fmt.Sprintf("node-%s-linux-%s.tar.gz", nodeRelease, arch), nil
}

// ensureNode provisions the Node distribution once per runner host. A
// <dir>.complete marker gates readers, so a crashed download never leaves a
// half-unpacked runtime for the next job.
var nodeProvisionMu sync.Mutex

func (e *executor) ensureNode(ctx context.Context) error {
	nodeProvisionMu.Lock()
	defer nodeProvisionMu.Unlock()
	dir := e.nodeHostDir()
	if _, err := os.Stat(dir + ".complete"); err == nil {
		return nil
	}
	archive, err := nodeArchive()
	if err != nil {
		return err
	}
	tmp := dir + fmt.Sprintf(".tmp-%d", os.Getpid())
	_ = os.RemoveAll(tmp)
	defer os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return err
	}
	tgz := filepath.Join(tmp, archive)
	if err := download(ctx, nodeDistBase+"/"+nodeRelease+"/"+archive, tgz); err != nil {
		return fmt.Errorf("download node %s: %w", nodeRelease, err)
	}
	if err := verifyDistSum(ctx, archive, tgz); err != nil {
		return err
	}
	if err := untarGz(tgz, tmp); err != nil {
		return fmt.Errorf("unpack node: %w", err)
	}
	_ = os.RemoveAll(dir)
	if err := os.Rename(filepath.Join(tmp, strings.TrimSuffix(archive, ".tar.gz")), dir); err != nil {
		return err
	}
	return os.WriteFile(dir+".complete", []byte(nodeRelease+"\n"), 0o644)
}

// verifyDistSum checks the tarball against the SHASUMS256.txt published with
// the release. The checksum file is fetched over https from the same origin;
// it proves download integrity, not publisher trust.
func verifyDistSum(ctx context.Context, archive, tgz string) error {
	sums := filepath.Join(filepath.Dir(tgz), "SHASUMS256.txt")
	if err := download(ctx, nodeDistBase+"/"+nodeRelease+"/SHASUMS256.txt", sums); err != nil {
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
func inputEnv(meta *actions.Meta, with map[string]any) map[string]string {
	env := map[string]string{}
	for name, in := range meta.Inputs {
		v := in.Default
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

// runNodeAction executes a JavaScript action inside the job container on the
// runner-provisioned Node runtime.
// ponytail: pre/post entrypoints are ignored (logged once); upgrade when a
// pre/post-capable action is actually needed.
func (e *executor) runNodeAction(ctx context.Context, step StepSpec, meta *actions.Meta, actionPath string) (string, int) {
	if err := e.ensureNode(ctx); err != nil {
		e.logStep(step, "KERN: node runtime: "+err.Error())
		return "failure", 1
	}
	main := strings.TrimSpace(meta.Runs.Main)
	if main == "" {
		e.logStep(step, "KERN: "+step.Uses+" declares no runs.main")
		return "failure", 1
	}
	if meta.Runs.Pre != "" || meta.Runs.Post != "" {
		e.logStep(step, "KERN: warning: pre/post entrypoints are not executed yet")
	}
	if meta.Runs.Using != "node20" {
		e.logStep(step, fmt.Sprintf("KERN: runs.using %s executes on the bundled %s runtime", meta.Runs.Using, nodeRelease))
	}
	env := map[string]string{}
	for k, v := range step.Env {
		env[k] = v
	}
	env["GITHUB_ACTION_PATH"] = actionPath
	for k, v := range inputEnv(meta, step.With) {
		env[k] = v
	}
	sub := StepSpec{
		Number:         step.Number,
		ID:             step.ID,
		Name:           step.Name,
		Run:            shellQuote(containerNodeBin()) + " " + shellQuote(actionPath+"/"+main),
		Shell:          e.shell,
		Env:            env,
		TimeoutSeconds: step.TimeoutSeconds,
	}
	outcome, code, err := e.runStepBody(ctx, sub)
	if err != nil {
		e.logStep(step, "KERN: "+err.Error())
	}
	return outcome, code
}

package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/dvorinka/kern/internal/update"
)

const selfUpdateHold = 15 * time.Minute

// planReplace builds the `docker run` argv that clones this container onto
// image. The previous container is left stopped under name+"-prev".
//
// ponytail: the stopped previous container is not removed, delete it once
// a second replace needs the same name.
func acceptImage(image string) error {
	base, digest, ok := strings.Cut(image, "@")
	if !ok {
		return fmt.Errorf("refusing image %q", image)
	}
	if _, err := update.PullRef(base, digest); err != nil {
		return fmt.Errorf("refusing image %q", image)
	}
	return nil
}

func planReplace(inspectJSON []byte, image string) (args []string, name, prev string, err error) {
	if err := acceptImage(image); err != nil {
		return nil, "", "", err
	}
	var in struct {
		Name   string `json:"Name"`
		Config struct {
			User       string            `json:"User"`
			Env        []string          `json:"Env"`
			Cmd        []string          `json:"Cmd"`
			Entrypoint []string          `json:"Entrypoint"`
			Labels     map[string]string `json:"Labels"`
		} `json:"Config"`
		HostConfig struct {
			Binds  []string `json:"Binds"`
			Mounts []struct {
				Type        string `json:"Type"`
				Source      string `json:"Source"`
				Target      string `json:"Target"`
				ReadOnly    bool   `json:"ReadOnly"`
				BindOptions *struct {
					Propagation string `json:"Propagation"`
				} `json:"BindOptions"`
			} `json:"Mounts"`
			NetworkMode string `json:"NetworkMode"`
			Privileged  bool   `json:"Privileged"`
			Memory      int64  `json:"Memory"`
			NanoCPUs    int64  `json:"NanoCPUs"`
			NanoCpus    int64  `json:"NanoCpus"`
			Devices     []struct {
				PathOnHost        string `json:"PathOnHost"`
				PathInContainer   string `json:"PathInContainer"`
				CgroupPermissions string `json:"CgroupPermissions"`
			} `json:"Devices"`
			SecurityOpt []string `json:"SecurityOpt"`
			ExtraHosts  []string `json:"ExtraHosts"`
			Restart     struct {
				Name              string `json:"Name"`
				MaximumRetryCount int    `json:"MaximumRetryCount"`
			} `json:"RestartPolicy"`
			Ports map[string][]struct {
				HostIP   string `json:"HostIp"`
				HostPort string `json:"HostPort"`
			} `json:"PortBindings"`
		} `json:"HostConfig"`
	}
	if err := json.Unmarshal(inspectJSON, &in); err != nil {
		return nil, "", "", fmt.Errorf("inspect: %w", err)
	}
	name = strings.TrimPrefix(in.Name, "/")
	if name == "" || strings.Contains(name, " ") {
		return nil, "", "", fmt.Errorf("container has no usable name")
	}
	sock := false
	for _, b := range in.HostConfig.Binds {
		if strings.Contains(b, "/var/run/docker.sock") {
			sock = true
		}
	}
	if !sock {
		return nil, "", "", fmt.Errorf("refusing to replace a container that does not mount docker.sock")
	}
	prev = name + "-prev"
	args = []string{"run", "-d", "--name", name}
	restart := in.HostConfig.Restart.Name
	if restart == "" {
		restart = "unless-stopped"
	}
	if restart == "on-failure" && in.HostConfig.Restart.MaximumRetryCount > 0 {
		restart = fmt.Sprintf("on-failure:%d", in.HostConfig.Restart.MaximumRetryCount)
	}
	args = append(args, "--restart", restart)
	if in.HostConfig.Privileged {
		args = append(args, "--privileged")
	}
	if net := in.HostConfig.NetworkMode; net != "" && net != "default" {
		args = append(args, "--network", net)
	}
	if in.Config.User != "" {
		args = append(args, "--user", in.Config.User)
	}
	for spec, binds := range in.HostConfig.Ports {
		port := spec // keep the /udp or /tcp suffix; stripping it changes the published protocol
		for _, b := range binds {
			pub := port
			if b.HostPort != "" {
				pub = b.HostPort + ":" + port
			}
			if b.HostIP != "" {
				pub = b.HostIP + ":" + pub
			}
			args = append(args, "-p", pub)
		}
	}
	seenMount := map[string]bool{}
	for _, b := range in.HostConfig.Binds {
		args = append(args, "-v", b)
		parts := strings.Split(b, ":")
		if len(parts) > 1 {
			seenMount[parts[1]] = true
		}
	}
	for _, m := range in.HostConfig.Mounts {
		if m.Target == "" || seenMount[m.Target] || (m.Type != "bind" && m.Type != "volume") {
			continue
		}
		spec := "type=" + m.Type + ",source=" + m.Source + ",target=" + m.Target
		if m.ReadOnly {
			spec += ",readonly"
		}
		if m.BindOptions != nil && m.BindOptions.Propagation != "" {
			spec += ",propagation=" + m.BindOptions.Propagation
		}
		args = append(args, "--mount", spec)
	}
	if in.HostConfig.Memory > 0 {
		args = append(args, "--memory", strconv.FormatInt(in.HostConfig.Memory, 10))
	}
	if nano := in.HostConfig.NanoCPUs + in.HostConfig.NanoCpus; nano > 0 {
		args = append(args, "--cpus", strconv.FormatFloat(float64(nano)/1e9, 'f', -1, 64))
	}
	for _, d := range in.HostConfig.Devices {
		dev := d.PathOnHost
		if d.PathInContainer != "" {
			dev += ":" + d.PathInContainer
		}
		if d.CgroupPermissions != "" {
			dev += ":" + d.CgroupPermissions
		}
		args = append(args, "--device", dev)
	}
	for _, opt := range in.HostConfig.SecurityOpt {
		args = append(args, "--security-opt", opt)
	}
	for _, host := range in.HostConfig.ExtraHosts {
		args = append(args, "--add-host", host)
	}
	for k, v := range in.Config.Labels {
		args = append(args, "--label", k+"="+v)
	}
	for _, e := range in.Config.Env {
		if e == "" {
			continue
		}
		args = append(args, "-e", e)
	}
	if len(in.Config.Entrypoint) > 0 {
		args = append(args, "--entrypoint", in.Config.Entrypoint[0])
	}
	args = append(args, image)
	if len(in.Config.Entrypoint) > 1 {
		args = append(args, in.Config.Entrypoint[1:]...)
	}
	args = append(args, in.Config.Cmd...)
	return args, name, prev, nil
}

var containerIDRe = regexp.MustCompile(`[0-9a-f]{64}`)

// inspectSelfContainer finds this container even when it was started with a
// custom --hostname. Docker's default hostname is the container id, but the
// custom case is only recoverable from the cgroup path.
func inspectSelfContainer(ctx context.Context) ([]byte, error) {
	candidates := []string{}
	if host, err := os.Hostname(); err == nil && host != "" {
		candidates = append(candidates, host)
	}
	for _, path := range []string{"/proc/1/cgroup", "/proc/self/cgroup"} {
		if data, err := os.ReadFile(path); err == nil {
			for _, id := range containerIDRe.FindAllString(string(data), -1) {
				candidates = append(candidates, id, id[:12])
			}
		}
	}
	seen := map[string]bool{}
	var last error
	for _, id := range candidates {
		if seen[id] {
			continue
		}
		seen[id] = true
		out, err := dockerOutput(ctx, "inspect", "--format", "{{json .}}", id)
		if err == nil {
			return out, nil
		}
		last = err
	}
	if last == nil {
		last = fmt.Errorf("no container id candidates found")
	}
	return nil, fmt.Errorf("inspect self container: %w", last)
}

// applySelfUpdate pulls image and replaces this container. busy is checked
// again after the pull so a job that arrived mid-pull is not killed.
// On success the current process is stopped by `docker stop` of the
// renamed container.
func applySelfUpdate(ctx context.Context, image string, busy func() bool) error {
	if err := acceptImage(image); err != nil {
		return err
	}
	if busy != nil && busy() {
		return fmt.Errorf("a job is running")
	}
	if err := dockerCombined(ctx, "pull", image); err != nil {
		return err
	}
	if busy != nil && busy() {
		return fmt.Errorf("a job started during the pull")
	}
	out, err := inspectSelfContainer(ctx)
	if err != nil {
		return err
	}
	args, name, prev, err := planReplace(out, image)
	if err != nil {
		return err
	}
	// A leftover previous container blocks the rename. Remove it only when
	// it is not running.
	if running, checkErr := dockerOutput(ctx, "inspect", "--format", "{{.State.Running}}", prev); checkErr == nil {
		if strings.TrimSpace(string(running)) == "true" {
			return fmt.Errorf("previous container %s is still running", prev)
		}
		_ = dockerCombined(ctx, "rm", "-f", prev)
	}
	if err := dockerCombined(ctx, "rename", name, prev); err != nil {
		return err
	}
	if err := dockerCombined(ctx, args...); err != nil {
		_ = dockerCombined(ctx, "rm", "-f", name)
		if back := dockerCombined(ctx, "rename", prev, name); back != nil {
			return fmt.Errorf("replace failed (%v) and restoring the name failed: %w", err, back)
		}
		_ = dockerCombined(ctx, "start", name)
		return err
	}
	_ = dockerCombined(ctx, "update", "--restart=no", prev)
	// Stopping prev stops this process. A failure here leaves both running.
	if err := dockerCombined(ctx, "stop", prev); err != nil {
		return err
	}
	return nil
}

func dockerCombined(ctx context.Context, args ...string) error {
	_, err := dockerOutput(ctx, args...)
	return err
}

func dockerOutput(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return out, fmt.Errorf("docker %s: %s", args[0], msg)
	}
	return out, nil
}

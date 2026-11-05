package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
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
			User       string   `json:"User"`
			Env        []string `json:"Env"`
			Cmd        []string `json:"Cmd"`
			Entrypoint []string `json:"Entrypoint"`
		} `json:"Config"`
		HostConfig struct {
			Binds       []string `json:"Binds"`
			NetworkMode string   `json:"NetworkMode"`
			Privileged  bool     `json:"Privileged"`
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
	for _, b := range in.HostConfig.Binds {
		args = append(args, "-v", b)
	}
	for spec, binds := range in.HostConfig.Ports {
		port := strings.Split(spec, "/")[0]
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
	for _, e := range in.Config.Env {
		if e == "" || strings.Contains(e, "\n") {
			continue
		}
		args = append(args, "-e", e)
	}
	if len(in.Config.Entrypoint) > 0 {
		args = append(args, "--entrypoint", in.Config.Entrypoint[0])
		if len(in.Config.Entrypoint) > 1 {
			return nil, "", "", fmt.Errorf("entrypoint %q is not a single command", in.Config.Entrypoint)
		}
	}
	args = append(args, image)
	args = append(args, in.Config.Cmd...)
	return args, name, prev, nil
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
	id, err := os.Hostname()
	if err != nil || id == "" {
		return fmt.Errorf("hostname: %w", err)
	}
	out, err := dockerOutput(ctx, "inspect", "--format", "{{json .}}", id)
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

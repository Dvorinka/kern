package workflows

// Service containers (jobs.<id>.services). KERN runs each service on a shared
// per-job Docker network where its service name is the network alias — the
// same access model GitHub gives container jobs (servicename:port).
// credentials and ports are honoured; volumes are not (host bind mounts from
// workflow YAML would expose the runner filesystem to untrusted runs), and
// `options` accepts only the --health-* docker flags, everything else warns.

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var serviceNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// validateService checks one services.<name> entry and warns on unsupported keys.
func validateService(name string, raw any) error {
	if !serviceNameRe.MatchString(name) {
		return fmt.Errorf("service name %q must start alphanumeric and contain only letters, digits, '_', '-', '.'", name)
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("service %q must be a mapping with an image", name)
	}
	img, _ := m["image"].(string)
	if strings.TrimSpace(img) == "" {
		return fmt.Errorf("service %q requires an image", name)
	}
	return nil
}

// serviceWarnings emits warnings for service keys KERN does not honour.
func serviceWarnings(d *diagnostics, path string, m map[string]any) {
	for k := range m {
		switch k {
		case "image", "env", "options", "ports", "credentials":
		case "volumes":
			d.warnf("services", path+".volumes", "service volumes are not implemented and are ignored")
		default:
			d.warnf("services", path+"."+k, fmt.Sprintf("service option %q is not implemented and is ignored", k))
		}
	}
}

var healthFlags = map[string]bool{
	"--health-cmd": true, "--health-interval": true, "--health-timeout": true,
	"--health-retries": true, "--health-start-period": true,
}

// compileService builds the runtime spec for one service. Call after
// validateService and serviceWarnings.
func compileService(d *diagnostics, path, name string, raw any) ServicePlan {
	m, _ := raw.(map[string]any)
	sp := ServicePlan{Name: name, Image: strings.TrimSpace(asString(m["image"]))}
	if env, ok := m["env"].(map[string]any); ok {
		sp.Env = map[string]string{}
		for k, v := range env {
			sp.Env[k] = asString(v)
		}
	}
	sp.Ports = compileServicePorts(d, path+".ports", m["ports"])
	sp.Credentials = compileCredentials(d, path+".credentials", m["credentials"])
	toks := shellWords(asString(m["options"]))
	for i := 0; i < len(toks); i++ {
		tok := toks[i]
		flag, _, eq := strings.Cut(tok, "=")
		if healthFlags[flag] {
			sp.Options = append(sp.Options, tok)
			if !eq && i+1 < len(toks) && !strings.HasPrefix(toks[i+1], "-") {
				i++ // --health-cmd takes its value as the next word
				sp.Options = append(sp.Options, toks[i])
			}
			continue
		}
		d.warnf("services", path+".options", fmt.Sprintf("service option %q is not honoured; only --health-* docker flags are", tok))
	}
	return sp
}

// compileServicePorts normalises services.<name>.ports to the container-side
// port numbers. GitHub maps "host:container" onto the runner's localhost; KERN
// shares the service's network namespace with the job container instead, so
// the service's own port is the one reachable on localhost. Host-side
// remapping (5433:5432) cannot be expressed that way and warns.
func compileServicePorts(d *diagnostics, path string, raw any) []string {
	var entries []string
	switch v := raw.(type) {
	case string:
		entries = []string{v}
	case []any:
		for _, e := range v {
			entries = append(entries, asString(e))
		}
	case nil:
		return nil
	default:
		d.warnf("services", path, "service ports must be a string or list of strings")
		return nil
	}
	var ports []string
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		parts := strings.Split(e, ":")
		container := strings.TrimSuffix(strings.TrimSuffix(parts[len(parts)-1], "/tcp"), "/udp")
		if n, err := strconv.Atoi(container); err != nil || n < 1 || n > 65535 {
			d.warnf("services", path, fmt.Sprintf("service port %q is not a plain port mapping and is ignored", e))
			continue
		}
		if len(parts) >= 2 {
			host := strings.TrimSuffix(strings.TrimSuffix(parts[len(parts)-2], "/tcp"), "/udp")
			if host != container {
				d.warnf("services", path, fmt.Sprintf(
					"service port %q maps a different host port; on KERN the service port %s is what localhost exposes", e, container))
			}
		}
		ports = append(ports, container)
	}
	return ports
}

// compileCredentials reads a credentials: {username, password} mapping.
// Both fields are required for a login; a partial map warns and is dropped
// rather than silently pulling unauthenticated.
func compileCredentials(d *diagnostics, path string, raw any) *RegistryCredentials {
	m, ok := raw.(map[string]any)
	if !ok {
		if raw != nil {
			d.warnf("services", path, "service credentials must be a mapping with username and password")
		}
		return nil
	}
	user, pass := asString(m["username"]), asString(m["password"])
	if strings.TrimSpace(user) == "" || strings.TrimSpace(pass) == "" {
		d.warnf("services", path, "service credentials require both username and password; ignored")
		return nil
	}
	return &RegistryCredentials{Username: user, Password: pass}
}

// shellWords splits a docker --options string honouring quotes — enough for
// --health-cmd "pg_isready -U x" style values.
func shellWords(s string) []string {
	var out []string
	var cur strings.Builder
	var quote rune
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
		case r == ' ' || r == '\t':
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

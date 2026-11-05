package workflows

// Service containers (jobs.<id>.services). KERN runs each service on a shared
// per-job Docker network where its service name is the network alias — the
// same access model GitHub gives container jobs (servicename:port).
// credentials/ports/volumes are not honoured; `options` accepts only the
// --health-* docker flags, everything else warns.

import (
	"fmt"
	"regexp"
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
		case "image", "env", "options":
		case "credentials":
			d.warnf("services", path+".credentials", "service registry credentials are not implemented and are ignored")
		case "ports":
			d.warnf("services", path+".ports", "service port publishing is not implemented; reach the service by its name on the job network")
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

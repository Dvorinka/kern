package runner

// GitHub Actions workflow commands: ::error::/::warning::/::notice::/::debug::
// lines emitted on step stdout/stderr become annotations. The parser handles
// the documented form `::cmd key=value,key=value::message` plus the bare
// `::cmd::message` form. Deprecated commands (set-env, add-path, stop-commands,
// echo) are deliberately not implemented — they are security hazards GitHub
// itself removed.

import (
	"bytes"
	"strconv"
	"strings"
	"sync"
)

// maxStepAnnotations caps stored annotations per step; GitHub itself accepts
// far more but a pathological job should not grow the steps row unboundedly.
const maxStepAnnotations = 100

// annotation mirrors the subset of GitHub's check-run annotation shape KERN
// stores: level, message, optional title and file/line/col span.
type annotation struct {
	Level   string `json:"level"`
	Message string `json:"message"`
	Title   string `json:"title,omitempty"`
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	Col     int    `json:"col,omitempty"`
	EndLine int    `json:"end_line,omitempty"`
	EndCol  int    `json:"end_col,omitempty"`
}

var annotationCommands = map[string]bool{
	"error": true, "warning": true, "notice": true, "debug": true,
}

// parseAnnotation parses one output line. Returns nil for non-command lines.
// The input should already be secret-masked; annotation text inherits that.
func parseAnnotation(line []byte) *annotation {
	// Commands start at column 0 modulo leading whitespace; anything else is
	// ordinary output. Require a closing "::" separator before end of line.
	trimmed := bytes.TrimLeft(line, " \t")
	if len(trimmed) < 5 || trimmed[0] != ':' || trimmed[1] != ':' {
		return nil
	}
	rest := trimmed[2:]
	// command name runs to first space or ':'
	i := 0
	for i < len(rest) && rest[i] != ' ' && rest[i] != ':' && rest[i] != '\t' {
		i++
	}
	if i == 0 || i >= len(rest) {
		return nil
	}
	level := string(rest[:i])
	if !annotationCommands[level] {
		return nil
	}
	rest = rest[i:]

	// rest is either "::message" or " key=value,...::message"
	var props, msg string
	sep := bytes.Index(rest, []byte("::"))
	if sep < 0 {
		return nil
	}
	props = string(rest[:sep])
	msg = string(rest[sep+2:])
	// A bare "::cmd::msg" has empty props; "::cmd ::msg" (space then ::) also
	// parses with empty props after trimming.
	props = strings.TrimSpace(props)
	msg = unescapeAnnotationMsg(strings.TrimRight(msg, "\r\n"))
	if msg == "" {
		return nil
	}

	a := &annotation{Level: level, Message: msg}
	if props != "" {
		applyAnnotationProps(a, props)
	}
	return a
}

// applyAnnotationProps decodes `key=value` pairs separated by commas. Values
// may contain ':' and '=' but not unescaped ',' — the toolkit escapes those.
func applyAnnotationProps(a *annotation, props string) {
	for _, pair := range strings.Split(props, ",") {
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) != 2 {
			continue
		}
		key := strings.TrimSpace(kv[0])
		val := unescapeAnnotationProp(kv[1])
		switch key {
		case "file":
			a.File = val
		case "line":
			a.Line = atoi(val)
		case "col":
			a.Col = atoi(val)
		case "endLine":
			a.EndLine = atoi(val)
		case "endColumn":
			a.EndCol = atoi(val)
		case "title":
			a.Title = val
		}
	}
}

// unescapeAnnotationMsg decodes the message escapes. Order matters: decode the
// CR/LF escapes before %25 so a literal "%0A" (encoded "%250A") survives.
func unescapeAnnotationMsg(s string) string {
	s = strings.ReplaceAll(s, "%0D", "\r")
	s = strings.ReplaceAll(s, "%0A", "\n")
	return strings.ReplaceAll(s, "%25", "%")
}

// unescapeAnnotationProp decodes property escapes: ',' ':' CR LF and '%'.
// Same ordering rule — multi-char escapes first, %25 last.
func unescapeAnnotationProp(s string) string {
	s = strings.ReplaceAll(s, "%0D", "\r")
	s = strings.ReplaceAll(s, "%0A", "\n")
	s = strings.ReplaceAll(s, "%3A", ":")
	s = strings.ReplaceAll(s, "%2C", ",")
	return strings.ReplaceAll(s, "%25", "%")
}

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

// annotationSink collects annotations across a step's output streams. Both
// the body pump and the uses-step pump write into the same sink; accesses are
// serialized by mu so concurrent streamInto goroutines stay safe.
type annotationSink struct {
	mu    sync.Mutex
	items []annotation
}

func (s *annotationSink) reset() {
	s.mu.Lock()
	s.items = s.items[:0]
	s.mu.Unlock()
}

func (s *annotationSink) add(a annotation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.items) >= maxStepAnnotations {
		return
	}
	s.items = append(s.items, a)
}

func (s *annotationSink) list() []annotation {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]annotation, len(s.items))
	copy(out, s.items)
	return out
}

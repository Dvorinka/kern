package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/term"
)

func colorEnabled() bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	return term.IsTerminal(int(os.Stdout.Fd()))
}

func ansi(code, s string) string {
	if !colorEnabled() || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func styleBold(s string) string   { return ansi("1", s) }
func styleDim(s string) string    { return ansi("2", s) }
func styleGreen(s string) string  { return ansi("32", s) }
func styleRed(s string) string    { return ansi("31", s) }
func styleYellow(s string) string { return ansi("33", s) }
func styleBlue(s string) string   { return ansi("34", s) }
func styleCyan(s string) string   { return ansi("36", s) }

// styleStatus tints common status/conclusion labels when stdout is a TTY.
func styleStatus(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "success", "online", "ok", "approved", "active", "processed":
		return styleGreen(s)
	case "failure", "failed", "offline", "error", "err", "denied", "rejected":
		return styleRed(s)
	case "in_progress", "running", "queued", "pending":
		return styleBlue(s)
	case "cancelled", "draining", "warn", "warning", "waiting_approval", "awaiting_approval", "skipped":
		return styleYellow(s)
	default:
		return s
	}
}

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// printNDJSON writes a single compact JSON line for streaming / --watch modes.
func printNDJSON(v any) {
	_ = json.NewEncoder(os.Stdout).Encode(v)
}

// table renders aligned columns via tabwriter.
func table(headers []string, rows [][]string) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	if colorEnabled() {
		styled := make([]string, len(headers))
		for i, h := range headers {
			styled[i] = styleDim(h)
		}
		fmt.Fprintln(w, strings.Join(styled, "\t"))
	} else {
		fmt.Fprintln(w, strings.Join(headers, "\t"))
	}
	for _, r := range rows {
		fmt.Fprintln(w, strings.Join(r, "\t"))
	}
	_ = w.Flush()
}

func rel(t *time.Time) string {
	if t == nil {
		return "-"
	}
	d := time.Since(*t)
	switch {
	case d < 0:
		return "now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return t.Format("2006-01-02")
	}
}

func relT(t time.Time) string { return rel(&t) }

// dur renders a start→end span; nil end means "still running".
func dur(start time.Time, end *time.Time) string {
	e := time.Now()
	if end != nil {
		e = *end
	}
	d := e.Sub(start)
	switch {
	case d < 0:
		return "0s"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%c", float64(n)/float64(div), "KMGT"[exp])
}

// orJSON prints v as JSON when --json was passed; otherwise fn renders a table.
func (c *cli) orJSON(v any, fn func()) {
	if c.json {
		printJSON(v)
		return
	}
	fn()
}

func strarg(args []string, i int, name string) (string, error) {
	if i >= len(args) {
		return "", fmt.Errorf("missing %s argument", name)
	}
	return args[i], nil
}

// openBrowser launches the OS default web browser for rawURL.
func openBrowser(rawURL string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", rawURL)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL)
	default:
		cmd = exec.Command("xdg-open", rawURL)
	}
	return cmd.Start()
}

// suggestCommand returns the closest command name within edit distance 2.
func suggestCommand(input string, candidates []string) string {
	best, bestDist := "", 3
	for _, cand := range candidates {
		d := editDistance(input, cand)
		if d < bestDist {
			bestDist = d
			best = cand
		}
	}
	return best
}

func editDistance(a, b string) int {
	la, lb := len(a), len(b)
	prev := make([]int, lb+1)
	cur := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		cur[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			del := prev[j] + 1
			ins := cur[j-1] + 1
			sub := prev[j-1] + cost
			m := del
			if ins < m {
				m = ins
			}
			if sub < m {
				m = sub
			}
			cur[j] = m
		}
		prev, cur = cur, prev
	}
	return prev[lb]
}

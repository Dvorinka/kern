package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"
)

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// table renders aligned columns via tabwriter.
func table(headers []string, rows [][]string) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, strings.Join(headers, "\t"))
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

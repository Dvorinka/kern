package main

// runDiagnosePager — the `kern diagnose -i` explorer: a raw-mode pager over
// the rendered bundle. Smallest honest interactive surface: j/k/arrows,
// PgUp/PgDn, g/G, q quits (#376).
// ponytail: no sections/search yet — upgrade if the bundle grows beyond
// "scroll to the failed step".

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

func runDiagnosePager(lines []string) error {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		fmt.Print(strings.Join(lines, "\n"), "\n")
		return nil
	}
	old, err := term.MakeRaw(fd)
	if err != nil {
		return err
	}
	defer term.Restore(fd, old)
	out := os.Stdout
	out.WriteString("\x1b[?1049h\x1b[?25l") // alt screen + hidden cursor
	defer out.WriteString("\x1b[?25h\x1b[?1049l")

	top, height := 0, 24
	_, h, err := term.GetSize(int(out.Fd()))
	if err == nil && h > 2 {
		height = h
	}
	render := func() {
		var b strings.Builder
		b.WriteString("\x1b[H")
		for i := top; i < min(top+height-1, len(lines)); i++ {
			line := lines[i]
			b.WriteString(line + "\x1b[K\r\n")
		}
		pct := 100
		if len(lines) > 1 {
			pct = min(100, 100*top/(len(lines)-1))
		}
		fmt.Fprintf(&b, "\x1b[7m diagnose — %d%% — j/k scroll · PgUp/PgDn · g/G ends · q quits \x1b[0m\x1b[K", pct)
		out.WriteString(b.String())
	}
	render()

	buf := make([]byte, 8)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			return err
		}
		for i := 0; i < n; i++ {
			switch buf[i] {
			case 'q', 3: // q / Ctrl-C
				return nil
			case 'j':
				top = min(top+1, max(0, len(lines)-height+1))
			case 'k':
				top = max(0, top-1)
			case 'g':
				top = 0
			case 'G':
				top = max(0, len(lines)-height+1)
			case 27: // escape sequence: arrows/PgUp/PgDn
				if i+2 < n && buf[i+1] == '[' {
					switch buf[i+2] {
					case 'A':
						top = max(0, top-1)
					case 'B':
						top = min(top+1, max(0, len(lines)-height+1))
					case '5', '6':
						if i+3 < n && buf[i+3] == '~' {
							if buf[i+2] == '5' {
								top = max(0, top-(height-1))
							} else {
								top = min(top+height-1, max(0, len(lines)-height+1))
							}
							i++
						}
					}
					i += 2
				}
			}
		}
		render()
	}
}

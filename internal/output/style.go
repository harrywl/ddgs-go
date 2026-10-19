// Package output implements the shared CLI output helpers of the ddgs
// command: colored printing, csv/json saving, query sanitizing and result
// downloading. Every function here mirrors a helper in the Python source of
// truth, ddgs/cli.py — see the "# mirrors ddgs/..." comment on each entry
// point.
package output

import (
	"os"
	"regexp"
	"strings"
	"unicode/utf8"
)

// ansiRe matches an ANSI escape sequence (click._compat._ansi_re).
var ansiRe = regexp.MustCompile("\x1b\\[(?:[0-9;:?]*[0-9;])*[a-zA-Z]")

// visibleLen returns the number of visible characters of s, ignoring ANSI
// escape sequences. Mirrors click._compat.term_len (which uses len() over the
// ANSI-stripped string, i.e. counts characters, not bytes).
func visibleLen(s string) int {
	return utf8.RuneCountInString(stripANSI(s))
}

// stripANSI removes ANSI escape sequences from s. Mirrors click.utils.strip_ansi.
func stripANSI(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}

// Color names as used by click.termui._ansi_colors (fg values).
const (
	fgBlack         = 30
	fgRed           = 31
	fgGreen         = 32
	fgYellow        = 33
	fgBlue          = 34
	fgMagenta       = 35
	fgCyan          = 36
	fgWhite         = 37
	fgBrightBlack   = 90
	fgBrightRed     = 91
	fgBrightGreen   = 92
	fgBrightYellow  = 93
	fgBrightBlue    = 94
	fgBrightMagenta = 95
	fgBrightCyan    = 96
	fgBrightWhite   = 97
)

// colorByIndex maps the Python COLORS dict (cli.py:25-42) to an ANSI
// foreground code. The dict index is the 1-based position of a result field;
// the value "white" (37) is the fallback for indexes past the dict, which
// Python would raise KeyError on — we degrade gracefully instead.
func colorByIndex(i int, noColor bool) string {
	if noColor {
		return "37" // click "white"
	}
	switch i {
	case 0:
		return itoa(fgBlack)
	case 1:
		return itoa(fgRed)
	case 2:
		return itoa(fgGreen)
	case 3:
		return itoa(fgYellow)
	case 4:
		return itoa(fgBlue)
	case 5:
		return itoa(fgMagenta)
	case 6:
		return itoa(fgCyan)
	case 7:
		return itoa(fgBrightBlack)
	case 8:
		return itoa(fgBrightRed)
	case 9:
		return itoa(fgBrightGreen)
	case 10:
		return itoa(fgBrightYellow)
	case 11:
		return itoa(fgBrightBlue)
	case 12:
		return itoa(fgBrightMagenta)
	case 13:
		return itoa(fgBrightCyan)
	case 14:
		return itoa(fgWhite)
	case 15:
		return itoa(fgBrightWhite)
	default:
		return itoa(fgWhite)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [24]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// style mirrors click.termui.style(text, fg, bg, overline): the escape codes
// are emitted in that exact order and terminated with a full reset.
func style(text, fg, bg string, overline bool) string {
	var sb strings.Builder
	if fg != "" {
		sb.WriteString("\x1b[" + fg + "m")
	}
	if bg != "" {
		sb.WriteString("\x1b[" + bg + "m")
	}
	if overline {
		sb.WriteString("\x1b[53m")
	}
	sb.WriteString(text)
	sb.WriteString("\x1b[0m")
	return sb.String()
}

// isTTY reports whether the file descriptor is an interactive terminal,
// mirroring click._compat.isatty (which additionally requires a non-`dumb`
// TERM — not reproduced here; the difference only shows in exotic setups).
func isTTY(f *os.File) bool {
	if f == nil {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// padRight mirrors Python's f"{title:<12}" — pad (never truncate) to 12
// characters.
func padRight(s string, width int) string {
	if visibleLen(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-visibleLen(s))
}

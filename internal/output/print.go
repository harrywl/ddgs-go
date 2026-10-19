package output

// Terminal output for the ddgs CLI, mirroring ddgs/cli.py::_print_data.

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	ddgs "github.com/harrywl/ddgs-go"
)

// wideFields are the result fields printed at 300 columns instead of 78,
// mirroring the width branch of _print_data:
//
//	width = 300 if k in ("content","href","image","source","thumbnail","url") else 78
var wideFields = map[string]bool{
	"content":   true,
	"href":      true,
	"image":     true,
	"source":    true,
	"thumbnail": true,
	"url":       true,
}

// PrintData writes results to stdout exactly like cli.py::_print_data:
// a numbered separator per result, then one line per result field, pausing for
// Enter between results when stdout is a terminal.
//
// Colours are disabled automatically when stdout is not a terminal (Python:
// `if not is_tty: no_color = True`); in that case click also strips the ANSI
// escapes entirely, so this implementation writes plain text off a TTY.
func PrintData(results []ddgs.Result, category string, noColor bool) {
	PrintDataTo(os.Stdout, os.Stdin, results, category, noColor)
}

// PrintDataTo is PrintData with injectable streams (used by tests).
func PrintDataTo(w io.Writer, in io.Reader, results []ddgs.Result, category string, noColor bool) {
	tty := isTTYWriter(w)
	if !tty {
		noColor = true
	}
	if len(results) == 0 {
		return
	}

	fields := fieldOrder(category, results)

	var reader *bufio.Reader
	if tty && in != nil {
		reader = bufio.NewReader(in)
	}
	// Python calls input() after every result in interactive mode.
	pause := func() {
		if reader == nil {
			return
		}
		_, _ = reader.ReadString('\n')
	}

	for i, r := range results {
		sep := fmt.Sprintf("%d.", i+1)
		if tty {
			sep = fmt.Sprintf("%d.\t    %s", i+1, strings.Repeat("=", 78))
			sep = style(sep, itoa(fgWhite), itoa(bgBlack), false)
		}
		fmt.Fprintln(w, sep)

		// Python's enumerate(e.items(), start=1) counts every field, empty or
		// not, so the colour index must do the same.
		for j, k := range fields {
			v, ok := r[k]
			if !ok {
				// e.items() only yields keys present in the dict.
				continue
			}
			title := k
			if k == "detected_language" {
				title = "language"
			}
			text := pyString(v)
			if isTruthy(v) {
				width := 78
				if wideFields[k] {
					width = 300
				}
				text = wrapText(text, width, 12)
			}
			line := padRight(title, 12) + text
			if tty {
				fg := itoa(fgWhite)
				if !noColor {
					fg = colorByIndex(j+1, false)
				}
				line = style(line, fg, itoa(bgBlack), true)
			}
			fmt.Fprintln(w, line)
		}
		pause()
	}
}

// bgBlack is the ANSI background code for black (click bg="black" → 40).
const bgBlack = 40

// isTTYWriter reports whether w is an interactive terminal. A *os.File that
// is redirected to a regular file is not (Python's sys.stdout.isatty()).
func isTTYWriter(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && isTTY(f)
}

// wrapText mirrors click.wrap_text(text, width, initial_indent="",
// subsequent_indent=" "*12, preserve_paragraphs=True):
//   - tabs are expanded, paragraphs are separated by empty lines and joined
//     with exactly one blank line,
//   - each paragraph is greedy-wrapped to `width` visible characters,
//   - every line after the first is prefixed with the subsequent indent.
func wrapText(text string, width, subsequentIndent int) string {
	if width <= 0 {
		width = 78
	}
	text = expandTabs(text)
	indent := strings.Repeat(" ", subsequentIndent)

	paras := splitParagraphs(text)
	if len(paras) == 0 {
		return ""
	}
	out := make([]string, 0, len(paras))
	for _, para := range paras {
		out = append(out, wrapParagraph(para, width, indent))
	}
	return strings.Join(out, "\n\n")
}

// expandTabs mirrors str.expandtabs() with the default tab size of 8.
func expandTabs(s string) string {
	if !strings.ContainsRune(s, '\t') {
		return s
	}
	var b strings.Builder
	col := 0
	for _, r := range s {
		switch r {
		case '\n', '\r':
			b.WriteRune(r)
			col = 0
		case '\t':
			n := 8 - col%8
			b.WriteString(strings.Repeat(" ", n))
			col += n
		default:
			b.WriteRune(r)
			col++
		}
	}
	return b.String()
}

// splitParagraphs splits text into paragraphs the way click's
// preserve_paragraphs branch does: empty lines delimit paragraphs (runs of
// empty lines collapse), leading/trailing empty lines are dropped, and the
// lines of a paragraph are joined with a single space.
func splitParagraphs(text string) []string {
	var paras []string
	var buf []string
	flush := func() {
		if len(buf) > 0 {
			paras = append(paras, strings.Join(buf, " "))
			buf = nil
		}
	}
	for _, line := range splitLines(text) {
		if line == "" {
			flush()
			continue
		}
		if len(buf) == 0 {
			// click preserves the indent of a paragraph's first line as
			// extra_indent; result fields never rely on it, so trim it.
			line = strings.TrimLeft(line, " \t")
		}
		buf = append(buf, line)
	}
	flush()
	return paras
}

// splitLines mirrors str.splitlines for the newline forms that can occur in
// HTTP-sourced text.
func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.Split(s, "\n")
}

// wrapParagraph flows one paragraph to width columns with the continuation
// indent applied from the second line on. textwrap counts the indent toward
// the width, so continuation lines hold width-len(indent) characters, and
// overlong words are broken (break_long_words=True).
func wrapParagraph(para string, width int, indent string) string {
	words := strings.Fields(para)
	if len(words) == 0 {
		return ""
	}
	contWidth := width - utf8.RuneCountInString(indent)
	if contWidth < 1 {
		contWidth = 1
	}

	var lines []string
	cur := ""
	limit := width
	flush := func() {
		if cur != "" {
			lines = append(lines, cur)
			cur = ""
			limit = contWidth
		}
	}

	for _, word := range words {
		// Break words longer than the available line width, filling the
		// current line first, like textwrap._handle_long_word.
		for utf8.RuneCountInString(word) > limit {
			runes := []rune(word)
			space := limit - utf8.RuneCountInString(cur)
			if cur != "" {
				space-- // the separating space
			}
			if space <= 0 {
				flush()
				continue
			}
			cur += string(runes[:space])
			word = string(runes[space:])
			flush()
		}

		candidate := word
		if cur != "" {
			candidate = cur + " " + word
		}
		if utf8.RuneCountInString(candidate) > limit && cur != "" {
			flush()
			cur = word
			continue
		}
		cur = candidate
	}
	flush()

	var sb strings.Builder
	for i, l := range lines {
		if i > 0 {
			sb.WriteString("\n")
			sb.WriteString(indent)
		}
		sb.WriteString(l)
	}
	return sb.String()
}

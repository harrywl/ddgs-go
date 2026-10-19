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
// a numbered separator per result, then one styled line per non-empty field,
// pausing for Enter between results when stdout is a TTY.
//
// Colors are disabled automatically when stdout is not a terminal (Python:
// `if not is_tty: no_color = True`) or when noColor is requested.
func PrintData(results []ddgs.Result, category string, noColor bool) {
	PrintDataTo(os.Stdout, os.Stdin, results, category, noColor)
}

// PrintDataTo is PrintData with injectable streams (used by tests).
func PrintDataTo(w io.Writer, in io.Reader, results []ddgs.Result, category string, noColor bool) {
	_, isTTY := w.(*os.File)
	if !isTTY {
		noColor = true
	}
	if len(results) == 0 {
		return
	}

	fields := ddgs.ResultFields(category)
	if len(fields) == 0 {
		fields = unionKeys(results)
	}

	pause := func() {
		if !isTTY || in == nil {
			return
		}
		// Python blocks on input() between results in interactive mode.
		bufio.NewReader(in).ReadString('\n')
	}

	for i, r := range results {
		var sep string
		if isTTY {
			sep = fmt.Sprintf("%d.\t    %s", i+1, strings.Repeat("=", 78))
		} else {
			sep = fmt.Sprintf("%d.", i+1)
		}
		// click.secho(sep, bg="black", fg="white")
		fmt.Fprintln(w, style(sep, itoa(fgWhite), itoa(bgBlack), false))

		j := 0 // 1-based field index for the color table, counting only printed fields
		for _, k := range fields {
			v := r[k]
			if isEmptyValue(v) {
				continue
			}
			j++

			title := k
			if k == "detected_language" {
				title = "language"
			}
			text := formatFieldValue(v)
			if text != "" {
				width := 78
				if wideFields[k] {
					width = 300
				}
				text = wrapText(text, width, 12)
			}

			fg := itoa(fgWhite)
			if !noColor {
				fg = colorByIndex(j, false)
			}
			fmt.Fprintln(w, style(padRight(title, 12)+text, fg, itoa(bgBlack), true))
		}
		pause()
	}
}

// bgBlack is the ANSI background code for black (click bg="black" → 40).
const bgBlack = 40

// isEmptyValue mirrors Python's `if v:` on a result field value.
func isEmptyValue(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case []byte:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	default:
		return false
	}
}

// formatFieldValue renders a field value as text, mirroring Python's f"{v}".
func formatFieldValue(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case []byte:
		return string(t)
	default:
		return fmt.Sprintf("%v", t)
	}
}

// wrapText mirrors click.wrap_text(text, width, initial_indent="",
// subsequent_indent=" "*12, preserve_paragraphs=True) closely enough for
// terminal display: words are flowed to `width` columns and every line after
// the first is indented by `subsequentIndent` spaces.
func wrapText(text string, width, subsequentIndent int) string {
	indent := strings.Repeat(" ", subsequentIndent)
	if width <= 0 {
		width = 78
	}

	// preserve_paragraphs: blank lines separate paragraphs and are kept.
	var out []string
	for _, para := range splitParagraphs(text) {
		out = append(out, wrapParagraph(para, width, indent))
	}
	return strings.Join(out, "\n")
}

// splitParagraphs splits on runs of blank lines, keeping the paragraph
// structure (click's preserve_paragraphs=True).
func splitParagraphs(text string) []string {
	paras := []string{}
	cur := []string{}
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			if len(cur) > 0 {
				paras = append(paras, strings.Join(cur, " "))
				cur = cur[:0]
			}
			paras = append(paras, "")
			continue
		}
		cur = append(cur, strings.TrimSpace(line))
	}
	if len(cur) > 0 {
		paras = append(paras, strings.Join(cur, " "))
	}
	return paras
}

// wrapParagraph flows one paragraph to width columns with the continuation
// indent applied from the second line on. click.wrap_text counts the indent
// toward the width, so continuation lines hold width-len(indent) characters.
func wrapParagraph(para string, width int, indent string) string {
	if para == "" {
		return ""
	}
	words := strings.Fields(para)
	if len(words) == 0 {
		return ""
	}
	contWidth := width - utf8.RuneCountInString(indent)
	if contWidth < 10 {
		contWidth = 10
	}

	var lines []string
	cur := ""
	limit := width // first line gets the full width
	for _, word := range words {
		candidate := word
		if cur != "" {
			candidate = cur + " " + word
		}
		if utf8.RuneCountInString(candidate) > limit && cur != "" {
			lines = append(lines, cur)
			cur = word
			limit = contWidth
			continue
		}
		cur = candidate
	}
	if cur != "" {
		lines = append(lines, cur)
	}

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

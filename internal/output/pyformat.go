package output

// Python value formatting helpers. cli.py prints values with an f-string
// (`f"{v}"`) and serialises CSV cells through csv.DictWriter, which calls
// str() on every value. Both mean: strings verbatim, None as "None", bools as
// True/False, and containers as Python reprs (`{'k': 'v'}`), not Go's
// `map[k:v]`. These helpers reproduce that formatting for the CLI.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// pyString mirrors Python's str()/f-string formatting of a result value.
func pyString(v any) string {
	switch t := v.(type) {
	case nil:
		return "None"
	case string:
		return t
	case []byte:
		return string(t)
	case bool:
		if t {
			return "True"
		}
		return "False"
	case map[string]any:
		return pyDictRepr(t)
	case []any:
		parts := make([]string, len(t))
		for i, e := range t {
			parts[i] = pyRepr(e)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		return pyFloat(t)
	default:
		return fmt.Sprint(t)
	}
}

// pyRepr mirrors Python's repr() for the scalar/container types a result can
// hold (dict values are quoted, unlike str() of the whole dict).
func pyRepr(v any) string {
	switch t := v.(type) {
	case string:
		return pyQuote(t)
	case map[string]any:
		return pyDictRepr(t)
	case []any:
		parts := make([]string, len(t))
		for i, e := range t {
			parts[i] = pyRepr(e)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	default:
		return pyString(v)
	}
}

// pyDictRepr renders a dict like Python's repr: {'key': 'value', ...}.
// Go maps are unordered, so keys are sorted for determinism; Python would
// preserve insertion order (unrecoverable from a Go map — documented
// divergence, only visible for videos' images/statistics fields).
func pyDictRepr(m map[string]any) string {
	if len(m) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(m))
	for _, k := range keys {
		parts = append(parts, pyQuote(k)+": "+pyRepr(m[k]))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// pyQuote renders a string the way Python's repr does: single quotes by
// default, double quotes when that avoids escaping, with the standard
// backslash escapes for control characters.
func pyQuote(s string) string {
	quote := byte('\'')
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		quote = '"'
	}
	var b strings.Builder
	b.WriteByte(quote)
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r == rune(quote) {
				b.WriteByte('\\')
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte(quote)
	return b.String()
}

// pyFloat formats a float like Python's str(): "1.0" rather than Go's "1".
func pyFloat(f float64) string {
	s := strconv.FormatFloat(f, 'g', -1, 64)
	if !strings.ContainsAny(s, ".eEnN") {
		s += ".0"
	}
	return s
}

// isTruthy mirrors Python's `if v:` test used by _print_data to decide
// whether a value is wrapped (falsy values are printed unwrapped).
func isTruthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case string:
		return t != ""
	case []byte:
		return len(t) > 0
	case bool:
		return t
	case map[string]any:
		return len(t) > 0
	case []any:
		return len(t) > 0
	case int:
		return t != 0
	case int64:
		return t != 0
	case float64:
		return t != 0
	default:
		return true
	}
}

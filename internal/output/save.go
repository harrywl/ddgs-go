package output

// File-output helpers for the ddgs CLI, mirroring ddgs/cli.py::_save_data,
// _save_json and _save_csv.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	ddgs "github.com/harrywl/ddgs-go"
)

// extraFieldOrder holds the Python dict key order for categories that are not
// library result types. extract.py returns {"url": ..., "content": ...} and
// cli.py saves that dict verbatim, so data[0].keys() is url, content.
var extraFieldOrder = map[string][]string{
	"extract": {"url", "content"},
}

// SaveData writes results to a CSV or JSON file, mirroring cli.py::_save_data.
//
// The Python logic is reproduced exactly:
//
//	filename, ext = filename.rsplit(".", 1) if filename.endswith((".csv",".json"))
//	                                    else (None, filename)
//	filename = filename or f"{function_name}_{query}_{utc:%Y%m%d_%H%M%S}"
//
// So `-o json` uses ext="json" with an auto-generated basename, while
// `-o out.json` writes to out.json. Any other extension is ignored: Python's
// _save_data has no else branch, so `-o out.txt` silently writes nothing and
// the command still succeeds. That quirk is preserved here (returned path is
// empty).
//
// category is the ddgs result category ("text", "images", ...); it supplies
// the CSV header / JSON key order, which in Python comes from
// data[0].keys() — i.e. the dataclass field order.
func SaveData(query string, results []ddgs.Result, functionName, category, filename string) (string, error) {
	base, ext := splitOutputFilename(filename)
	if base == "" {
		base = autoBaseName(functionName, query)
	}
	full := base + "." + ext

	switch ext {
	case "csv":
		if err := SaveCSV(full, results, category); err != nil {
			return "", err
		}
	case "json":
		if err := SaveJSON(full, results, category); err != nil {
			return "", err
		}
	default:
		return "", nil
	}
	return full, nil
}

// autoBaseName builds cli.py's default file stem:
// f"{function_name}_{query}_{datetime.now(tz=timezone.utc):%Y%m%d_%H%M%S}".
func autoBaseName(functionName, query string) string {
	return fmt.Sprintf("%s_%s_%s", functionName, query,
		time.Now().UTC().Format("20060102_150405"))
}

// splitOutputFilename mirrors the rsplit/endswith logic of _save_data.
// It returns (basename, extension); basename is empty when the extension has
// to be auto-generated.
func splitOutputFilename(filename string) (base, ext string) {
	if filename == "" {
		return "", ""
	}
	if strings.HasSuffix(filename, ".csv") || strings.HasSuffix(filename, ".json") {
		i := strings.LastIndex(filename, ".")
		return filename[:i], filename[i+1:]
	}
	// Python: ext = filename (e.g. "csv" / "json"), basename auto-generated.
	return "", filename
}

// SaveJSON writes results as an indented JSON array, mirroring _save_json
// (`json.dumps(data, ensure_ascii=False, indent=2)`): no trailing newline,
// keys in Python dataclass order, non-ASCII left unescaped.
//
// Field order is the Python dataclass order (ddgs.ResultFields), which a plain
// json.Marshal of a Go map cannot produce — encoding/json sorts map keys
// alphabetically. The array is therefore assembled with an ordered encoder.
func SaveJSON(path string, results []ddgs.Result, category string) error {
	items := make([]json.RawMessage, 0, len(results))
	for _, r := range results {
		raw, err := marshalOrdered(r, category)
		if err != nil {
			return err
		}
		items = append(items, raw)
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // ensure_ascii=False equivalent for HTML chars
	enc.SetIndent("", "  ")  // indent=2
	if err := enc.Encode(items); err != nil {
		return err
	}
	// Python's json.dumps does not append a newline; Encoder.Encode does.
	out := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	return os.WriteFile(path, out, 0o644)
}

// marshalOrdered serialises one Result as a JSON object with keys in the
// Python dataclass field order, falling back to first-seen map order for any
// keys outside the known field list.
func marshalOrdered(r ddgs.Result, category string) (json.RawMessage, error) {
	keys := fieldOrder(category, []ddgs.Result{r})
	seen := make(map[string]bool, len(keys))
	for _, k := range keys {
		seen[k] = true
	}
	// Keys the engine added beyond the dataclass fields (e.g. videos' extra
	// metadata) still have to appear; append them in sorted order for
	// determinism.
	var extra []string
	for k := range r {
		if !seen[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	keys = append(keys, extra...)

	var buf bytes.Buffer
	buf.WriteByte('{')
	first := true
	for _, k := range keys {
		v, ok := r[k]
		if !ok {
			continue
		}
		if !first {
			buf.WriteByte(',')
		}
		first = false
		kb, err := marshalNoEscape(k)
		if err != nil {
			return nil, err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		vb, err := marshalNoEscape(v)
		if err != nil {
			return nil, err
		}
		buf.Write(vb)
	}
	buf.WriteByte('}')
	return json.RawMessage(buf.Bytes()), nil
}

// marshalNoEscape encodes one JSON value with ensure_ascii=False semantics:
// encoding/json's default HTML escaping (\u003c, \u003e, \u0026) is disabled,
// matching Python's json.dumps(..., ensure_ascii=False).
func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// SaveCSV writes results with a header row, mirroring _save_csv: Python opens
// the file with newline="" and uses csv.DictWriter's default excel dialect —
// delimiter ",", quotechar '"', QUOTE_MINIMAL and lineterminator "\r\n".
// encoding/csv cannot reproduce that byte-for-byte (it converts newlines
// inside quoted fields to CRLF and quotes leading spaces), so the rows are
// written directly.
//
// Like Python, an empty result set produces an empty file (the `if data:`
// guard around writeheader).
func SaveCSV(path string, results []ddgs.Result, category string) error {
	var buf bytes.Buffer
	if len(results) > 0 {
		fields := fieldOrder(category, results)
		writeCSVRow(&buf, fields)
		for _, r := range results {
			row := make([]string, len(fields))
			for i, k := range fields {
				if v, ok := r[k]; ok {
					row[i] = pyString(v)
				}
				// Missing key: csv.DictWriter's restval ("").
			}
			writeCSVRow(&buf, row)
		}
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// writeCSVRow writes one CRLF-terminated record with minimal quoting.
func writeCSVRow(buf *bytes.Buffer, row []string) {
	for i, field := range row {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.WriteString(csvQuote(field))
	}
	buf.WriteString("\r\n")
}

// csvQuote implements Python's QUOTE_MINIMAL: quote only when the field holds
// the delimiter, the quotechar, '\r' or '\n'; double embedded quotes.
func csvQuote(s string) string {
	if !strings.ContainsAny(s, ",\"\r\n") {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// fieldOrder returns the ordered field names used for JSON objects, CSV
// headers and printed columns. It mirrors cli.py's data[0].keys(): the Python
// dataclass field order when the category is known, a fixed order for
// extract, and a deterministic (sorted) fallback otherwise.
func fieldOrder(category string, results []ddgs.Result) []string {
	if f := ddgs.ResultFields(category); len(f) > 0 {
		return f
	}
	if f, ok := extraFieldOrder[category]; ok {
		return f
	}
	return unionKeys(results)
}

// unionKeys collects the keys of all results in sorted order, used when the
// category is unknown (Go maps are unordered, so first-seen order is not
// retrievable).
func unionKeys(results []ddgs.Result) []string {
	seen := map[string]bool{}
	for _, r := range results {
		for k := range r {
			seen[k] = true
		}
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// EnsureDir creates a directory (and parents), mirroring
// Path(path).mkdir(parents=True, exist_ok=True).
func EnsureDir(path string) error {
	return os.MkdirAll(path, 0o755)
}

// JoinPath is filepath.Join, exported so the CLI does not import path/filepath.
func JoinPath(parts ...string) string { return filepath.Join(parts...) }

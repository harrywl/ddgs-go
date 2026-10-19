package output

// File-output helpers for the ddgs CLI, mirroring ddgs/cli.py::_save_data,
// _save_json and _save_csv.

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	ddgs "github.com/harrywl/ddgs-go"
)

// SaveData writes results to a CSV or JSON file, mirroring cli.py::_save_data.
//
// The Python logic is reproduced exactly:
//
//	filename, ext = filename.rsplit(".", 1) if filename.endswith((".csv",".json"))
//	                                    else (None, filename)
//	filename = filename or f"{function_name}_{query}_{utc:%Y%m%d_%H%M%S}"
//
// So `-o json` uses ext="json" with an auto-generated basename, while
// `-o out.json` writes to out.json. Any other extension is ignored (Python
// writes nothing in that case).
//
// category is the ddgs result category ("text", "images", ...); it supplies
// the CSV header / JSON key order, which in Python comes from
// data[0].keys() — i.e. the dataclass field order.
func SaveData(query string, results []ddgs.Result, functionName, category, filename string) (string, error) {
	base, ext := splitOutputFilename(filename)
	if base == "" {
		base = fmt.Sprintf("%s_%s_%s", functionName, query,
			time.Now().UTC().Format("20060102_150405"))
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
		// Python's _save_data has no else branch: an unrecognised extension
		// silently writes nothing. Report it instead of pretending success.
		return "", fmt.Errorf("unsupported output format %q (use csv, json, name.csv or name.json)", ext)
	}
	return full, nil
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
// (json.dumps(data, ensure_ascii=False, indent=2)).
//
// Field order is the Python dataclass order (ddgs.ResultFields), which a plain
// json.Marshal of a Go map cannot produce — encoding/json sorts map keys
// alphabetically. The array is therefore assembled with an ordered encoder.
func SaveJSON(path string, results []ddgs.Result, category string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false) // ensure_ascii=False equivalent for HTML chars
	enc.SetIndent("", "  ")  // indent=2

	// Wrap the pre-serialised objects so the outer array gets indenting while
	// each element keeps its field order.
	items := make([]json.RawMessage, 0, len(results))
	for _, r := range results {
		raw, err := marshalOrdered(r, category)
		if err != nil {
			return err
		}
		items = append(items, raw)
	}
	if err := enc.Encode(items); err != nil {
		return err
	}
	return f.Sync()
}

// marshalOrdered serialises one Result as a JSON object with keys in the
// Python dataclass field order, falling back to first-seen map order for any
// keys outside the known field list.
func marshalOrdered(r ddgs.Result, category string) (json.RawMessage, error) {
	keys := ddgs.ResultFields(category)
	if len(keys) == 0 {
		keys = unionKeys([]ddgs.Result{r})
	}
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
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		vb, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		buf.Write(vb)
	}
	buf.WriteByte('}')
	return json.RawMessage(buf.Bytes()), nil
}

// SaveCSV writes results with a header row, mirroring _save_csv
// (csv.DictWriter with fieldnames=data[0].keys(), QUOTE_MINIMAL — the
// encoding/csv default).
func SaveCSV(path string, results []ddgs.Result, category string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	w := csv.NewWriter(f)
	fields := ddgs.ResultFields(category)
	if len(fields) == 0 {
		fields = unionKeys(results)
	}
	if err := w.Write(fields); err != nil {
		return err
	}
	for _, r := range results {
		row := make([]string, len(fields))
		for i, k := range fields {
			row[i] = csvValue(r[k])
		}
		if err := w.Write(row); err != nil {
			return err
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return err
	}
	return f.Sync()
}

// unionKeys collects the keys of all results in first-seen order, used when
// the category is unknown.
func unionKeys(results []ddgs.Result) []string {
	seen := map[string]bool{}
	var keys []string
	for _, r := range results {
		for k := range r {
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	return keys
}

// csvValue renders one cell. Python's csv writer stringifies values; nested
// dicts (videos' images/statistics) become their JSON form, matching what
// str() would show closely enough for a data file.
func csvValue(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case []byte:
		return string(t)
	default:
		if b, err := json.Marshal(t); err == nil {
			return string(b)
		}
		return fmt.Sprintf("%v", t)
	}
}

// EnsureDir creates a directory (and parents), mirroring
// Path(path).mkdir(parents=True, exist_ok=True).
func EnsureDir(path string) error {
	return os.MkdirAll(path, 0o755)
}

// JoinPath is filepath.Join, exported so the CLI does not import path/filepath.
func JoinPath(parts ...string) string { return filepath.Join(parts...) }

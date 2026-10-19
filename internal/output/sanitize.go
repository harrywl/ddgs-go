package output

import "strings"

// SanitizeQuery turns a free-text query into a filesystem-friendly token. It
// mirrors ddgs/cli.py::_sanitize_query (cli.py:101-110) exactly: the same
// ordered plain-text replacements, applied one after another. Note the Python
// order matters ("site" is removed after ":" so "site:x" becomes "x"), and the
// final replace(" ", "") is a no-op because spaces were already turned into
// underscores — it is kept so the sequence reads like the source.
func SanitizeQuery(query string) string {
	q := strings.ReplaceAll(query, "filetype", "")
	q = strings.ReplaceAll(q, ":", "")
	q = strings.ReplaceAll(q, `"`, "'")
	q = strings.ReplaceAll(q, "site", "")
	q = strings.ReplaceAll(q, " ", "_")
	q = strings.ReplaceAll(q, "/", "_")
	q = strings.ReplaceAll(q, `\`, "_")
	q = strings.ReplaceAll(q, " ", "")
	return q
}

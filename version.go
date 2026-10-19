package ddgs

// Version is the published version of the Go port.
//
// The Python upstream (deedy5/ddgs) reports __version__ = "9.16.0"
// (ddgs/__init__.py:11). The "-go.1" suffix marks the revision of this Go
// port, as mandated by SPEC §B2. This is the ONLY root-package file added by
// stream B — every other root *.go file belongs to stream A.
const Version = "9.16.0-go.1"

// LibraryVersion returns the library version string, mirroring Python's
// ddgs.__version__ accessor. The `ddgs version` CLI command and the API/MCP
// servers (serverInfo.version) both report this value.
func LibraryVersion() string { return Version }

package output

// Result downloading for the ddgs CLI, mirroring ddgs/cli.py::_download_results
// and _download_file.

import (
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	ddgs "github.com/harrywl/ddgs-go"
)

// downloadTimeout mirrors the 10s timeout cli.py::_download_file passes to
// primp.Client.
const downloadTimeout = 10 * time.Second

// maxFilenameLen mirrors cli.py's `filename[:200]` truncation.
const maxFilenameLen = 200

// DownloadResults saves the media referenced by search results, mirroring
// _download_results: the target directory is `dir` when given, otherwise
// "{funcName}_{query}_{utc:%Y%m%d_%H%M%S}"; the URL is result["image"] for
// images and result["href"] otherwise; the file name is
// "{i}_{unquote(last path segment before '?')}".
//
// Downloads run on a bounded pool of `threads` workers (Python uses a
// ThreadPoolExecutor with max_workers=threads, default 10). Individual
// failures are reported to stderr and skipped, exactly as Python's
// _download_file swallows exceptions into a debug log.
func DownloadResults(query string, results []ddgs.Result, funcName, category, proxy, dir string, threads int, verify bool) (string, int, error) {
	if len(results) == 0 {
		return "", 0, nil
	}

	dirPath := dir
	if dirPath == "" {
		dirPath = fmt.Sprintf("%s_%s_%s", funcName, query,
			time.Now().UTC().Format("20060102_150405"))
	}
	if err := EnsureDir(dirPath); err != nil {
		return "", 0, err
	}
	if threads <= 0 {
		threads = 10 // cli.py: threads = 10 if threads is None else threads
	}

	client, err := downloadClient(proxy, verify)
	if err != nil {
		return "", 0, err
	}

	// Python picks the media URL per function name: images use "image",
	// everything else uses "href".
	urlField := "href"
	if funcName == "images" {
		urlField = "image"
	}

	type job struct {
		index    int
		fileURL  string
		filename string
	}
	jobs := make([]job, 0, len(results))
	for i, r := range results {
		raw, _ := r[urlField].(string)
		if raw == "" {
			continue
		}
		jobs = append(jobs, job{
			index:    i + 1, // cli.py enumerates from 1
			fileURL:  raw,
			filename: downloadFilename(raw),
		})
	}
	if len(jobs) == 0 {
		return dirPath, 0, nil
	}

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		done int
	)
	sem := make(chan struct{}, threads)
	total := len(jobs)

	for _, j := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func(j job) {
			defer wg.Done()
			defer func() { <-sem }()

			name := fmt.Sprintf("%d_%s", j.index, j.filename)
			if err := downloadFile(client, j.fileURL, dirPath, name); err != nil {
				// cli.py logs at debug level and keeps going.
				fmt.Fprintf(os.Stderr, "download_file url=%s: %v\n", j.fileURL, err)
			}
			mu.Lock()
			done++
			n := done
			mu.Unlock()
			printDownloadProgress(n, total)
		}(j)
	}
	wg.Wait()
	fmt.Fprintln(os.Stderr) // finish the progress line

	return dirPath, done, nil
}

// downloadFilename mirrors cli.py's
// `unquote(url.split("/")[-1].split("?")[0])`.
func downloadFilename(raw string) string {
	name := raw
	if i := strings.Index(name, "?"); i >= 0 {
		name = name[:i]
	}
	name = path.Base(name)
	if decoded, err := url.QueryUnescape(name); err == nil {
		name = decoded
	}
	if name == "" || name == "." || name == "/" {
		name = "download"
	}
	// Guard against path traversal in a remote-supplied name.
	name = strings.ReplaceAll(name, "..", "_")
	if r := []rune(name); len(r) > maxFilenameLen {
		name = string(r[:maxFilenameLen])
	}
	return name
}

// downloadClient builds the HTTP client used for downloads: proxy, TLS
// verification and the 10s timeout of cli.py::_download_file.
func downloadClient(proxy string, verify bool) (*http.Client, error) {
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment}
	if proxy != "" {
		u, err := url.Parse(proxy)
		if err != nil {
			return nil, fmt.Errorf("invalid proxy %q: %w", proxy, err)
		}
		transport.Proxy = http.ProxyURL(u)
	}
	if !verify {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // #nosec G402 — user-requested via -v false
	}
	return &http.Client{Transport: transport, Timeout: downloadTimeout}, nil
}

// downloadFile mirrors _download_file: GET the URL and write the body to
// dir/filename only on HTTP 200.
func downloadFile(client *http.Client, rawURL, dir, filename string) error {
	resp, err := client.Get(rawURL) // #nosec G107 — URL comes from search results
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(path.Join(dir, filename))
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

// printDownloadProgress renders a simple one-line progress indicator, standing
// in for click.progressbar (label "Downloading", show_percent, show_pos).
func printDownloadProgress(done, total int) {
	if total <= 0 {
		return
	}
	pct := done * 100 / total
	bar := 50 // click.progressbar(width=50)
	filled := pct * bar / 100
	if filled > bar {
		filled = bar
	}
	fmt.Fprintf(os.Stderr, "\rDownloading [%s%s] %d/%d %3d%%",
		strings.Repeat("#", filled), strings.Repeat("-", bar-filled), done, total, pct)
}

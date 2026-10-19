// Package apiserver is the Go port of ddgs/api_server/api.py — the FastAPI
// REST wrapper around the DDGS metasearch library, implemented on stdlib
// net/http with Go 1.22 method+pattern routing (no framework dependency).
//
// # Endpoints (mirror api.py exactly)
//
//	GET  /              -> HealthResponse{status, version, service}
//	GET  /health        -> HealthResponse
//	POST /search/text   body TextSearchRequest    -> SearchResponse{results}
//	GET  /search/text   ?query&region&safesearch&timelimit&max_results&page&backend
//	POST /search/images body ImagesSearchRequest  -> SearchResponse
//	GET  /search/images (+ the text params, plus size,color,type_image,layout,license_image)
//	POST /search/news   body NewsSearchRequest    -> SearchResponse
//	GET  /search/news   (same params as text)
//	POST /search/videos body VideosSearchRequest  -> SearchResponse
//	GET  /search/videos (+ the text params, plus resolution,duration,license_videos)
//	POST /search/books  body BooksSearchRequest   -> SearchResponse
//	GET  /search/books  ?query&max_results&page&backend
//	POST /extract       body ExtractRequest{url,format} -> {url,content}
//	GET  /extract       ?url&fmt                        -> {url,content}
//
// The per-category filters (images size/color/type_image/layout/license_image,
// videos resolution/duration/license_videos) are forwarded to the library as
// SearchOptions.Extra, whose keys match the **kwargs names the Python engines
// read.
//
// Error responses use FastAPI's shape, {"detail": "..."}:
//
//	400  empty/missing query, undecodable body, bad int param, missing url
//	404  unknown path ("Not Found")
//	405  known path, unsupported method ("Method Not Allowed")
//	429  ddgs.ErrRatelimit (Python RatelimitException)
//	500  any other library error, prefixed like api.py
//	     ("Search failed: ...", "Image search failed: ...", ...)
//
// Handlers depend on searchsvc.Searcher rather than *ddgs.DDGS so tests can
// inject a stub without touching the network.
package apiserver

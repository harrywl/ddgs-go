// Package searchsvc provides a minimal search interface shared by the API
// server (internal/apiserver) and the MCP server (internal/mcpserver).
//
// Handlers depend on Searcher instead of *ddgs.DDGS directly so unit tests
// can inject a stub without touching the network. The production
// implementation is the *ddgs.DDGS type itself (it already satisfies this
// interface — see adapter below).
package searchsvc

import (
	"context"

	ddgs "github.com/harrywl/ddgs-go"
)

// Searcher mirrors the five search entry points of *ddgs.DDGS plus Extract.
// Field names match ddgs.SearchOptions / ddgs.ExtractFormat so the servers
// can forward request payloads 1:1.
type Searcher interface {
	Text(ctx context.Context, query string, opts *ddgs.SearchOptions) ([]ddgs.Result, error)
	Images(ctx context.Context, query string, opts *ddgs.SearchOptions) ([]ddgs.Result, error)
	News(ctx context.Context, query string, opts *ddgs.SearchOptions) ([]ddgs.Result, error)
	Videos(ctx context.Context, query string, opts *ddgs.SearchOptions) ([]ddgs.Result, error)
	Books(ctx context.Context, query string, opts *ddgs.SearchOptions) ([]ddgs.Result, error)
	Extract(ctx context.Context, url string, format ddgs.ExtractFormat) (*ddgs.Extracted, error)
}

// DDGS adapts *ddgs.DDGS to Searcher. *ddgs.DDGS already has exactly these
// methods, so the adapter is a type alias wrapper kept explicit for clarity
// and to freeze the interface contract at compile time.
type DDGS struct{ *ddgs.DDGS }

// Compile-time check that the adapter satisfies Searcher.
var _ Searcher = DDGS{}

// New wraps a *ddgs.DDGS as a Searcher.
func New(d *ddgs.DDGS) Searcher { return DDGS{d} }

package renseijin

import (
	"net/http"
)

// Option configures a Register call.
type Option func(*config)

type config struct {
	httpClient *http.Client
	baseURL    string // overrides the spec's first server URL when non-empty
	namePrefix string
}

// WithHTTPClient supplies the *http.Client used to make outbound requests.
//
// Auth lives here: pass an http.Client whose Transport adds whatever
// credentials the upstream API needs (bearer token, signed requests, mTLS,
// ...). The library never reads or stores credentials itself.
//
// If nil, http.DefaultClient is used.
func WithHTTPClient(c *http.Client) Option {
	return func(cfg *config) { cfg.httpClient = c }
}

// WithBaseURL overrides the server URL taken from the OpenAPI document. Useful
// for pointing the generated tools at a sandbox, staging environment, or local
// mock without modifying the spec.
func WithBaseURL(u string) Option {
	return func(cfg *config) { cfg.baseURL = u }
}

// WithToolNamePrefix prepends a string to every generated tool name. Helpful
// when registering several APIs onto the same MCP server to avoid collisions.
func WithToolNamePrefix(p string) Option {
	return func(cfg *config) { cfg.namePrefix = p }
}

func newConfig(opts []Option) *config {
	cfg := &config{httpClient: http.DefaultClient}
	for _, o := range opts {
		o(cfg)
	}
	if cfg.httpClient == nil {
		cfg.httpClient = http.DefaultClient
	}
	return cfg
}

package renseijin

import (
	"maps"
	"net/http"
)

// Option configures a Register call.
type Option func(*config)

type config struct {
	httpClient *http.Client
	baseURL    string // overrides the spec's first server URL when non-empty
	namePrefix string

	// serverVariables override values for {var} placeholders found in the
	// spec's servers[].url. Anything not overridden falls back to the
	// ServerVariable.Default declared in the spec.
	serverVariables map[string]string

	// maxResponseBytes caps how much of an upstream response body is folded
	// into the tool result. Bodies larger than this are truncated with a
	// trailing "(truncated, N of M bytes shown)" marker so the model knows
	// it's looking at a partial view rather than the whole payload.
	//
	// 0 disables truncation entirely.
	maxResponseBytes int
}

// DefaultMaxResponseBytes is the cap applied when the caller does not pass
// WithMaxResponseBytes. Most chat models choke on a multi-MB JSON dump; 64 KiB
// keeps the context window usable while still surfacing enough body for the
// model to reason about success / failure shapes.
const DefaultMaxResponseBytes = 64 * 1024

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
//
// When set, WithBaseURL takes precedence over spec-derived URL resolution —
// including server-variable substitution. The string is sent through to
// url.Parse as-is, so it is the caller's responsibility to fill in any
// placeholders.
func WithBaseURL(u string) Option {
	return func(cfg *config) { cfg.baseURL = u }
}

// WithToolNamePrefix prepends a string to every generated tool name. Helpful
// when registering several APIs onto the same MCP server to avoid collisions.
func WithToolNamePrefix(p string) Option {
	return func(cfg *config) { cfg.namePrefix = p }
}

// WithServerVariables supplies overrides for the {var} placeholders in the
// spec's servers[].url. A spec like
//
//	servers:
//	- url: https://{region}.api.example.com/{version}
//	  variables:
//	    region:  {default: us-east-1}
//	    version: {default: v1}
//
// resolves to https://us-east-1.api.example.com/v1 by default. Pass
// WithServerVariables(map[string]string{"region": "eu-west-1"}) to redirect
// without editing the spec.
//
// If a placeholder has neither a caller override nor a spec-side default,
// Register returns an error rather than letting the unresolved URL reach the
// wire.
func WithServerVariables(vars map[string]string) Option {
	return func(cfg *config) {
		if cfg.serverVariables == nil {
			cfg.serverVariables = map[string]string{}
		}
		maps.Copy(cfg.serverVariables, vars)
	}
}

// WithMaxResponseBytes caps the upstream response body folded into the tool
// result. Pass 0 to disable truncation. See [DefaultMaxResponseBytes] for the
// out-of-the-box value.
func WithMaxResponseBytes(n int) Option {
	return func(cfg *config) { cfg.maxResponseBytes = n }
}

func newConfig(opts []Option) *config {
	cfg := &config{
		httpClient:       http.DefaultClient,
		maxResponseBytes: DefaultMaxResponseBytes,
	}
	for _, o := range opts {
		o(cfg)
	}
	if cfg.httpClient == nil {
		cfg.httpClient = http.DefaultClient
	}
	return cfg
}

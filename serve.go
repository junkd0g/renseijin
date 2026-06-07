package renseijin

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ServeOption configures a [Serve] call.
type ServeOption func(*serveConfig)

type serveConfig struct {
	name      string
	version   string
	transport mcp.Transport

	// register holds the Register-time options forwarded to the internal
	// Register call (auth, base-url override, tool-name prefix, ...).
	register []Option
}

// WithServerInfo sets the MCP implementation name and version advertised on
// the initialize handshake. Defaults to "renseijin"/"0.0.0".
func WithServerInfo(name, version string) ServeOption {
	return func(c *serveConfig) {
		c.name = name
		c.version = version
	}
}

// WithTransport selects the MCP transport [Serve] runs on. Defaults to
// &mcp.StdioTransport{} so the zero-config call serves a stdio-launched
// child process (the most common MCP wiring today).
//
// Use mcp.NewLoggingTransport(...) to dump JSON-RPC frames during debugging,
// or supply a streamable HTTP transport when the consumer connects over the
// wire instead of stdio.
func WithTransport(t mcp.Transport) ServeOption {
	return func(c *serveConfig) { c.transport = t }
}

// WithRegisterOptions forwards [Option] values to the internal [Register]
// call. Use it to pass WithHTTPClient, WithBaseURL, WithToolNamePrefix, and
// the rest — Serve does not redeclare them so the surface stays small.
func WithRegisterOptions(opts ...Option) ServeOption {
	return func(c *serveConfig) { c.register = opts }
}

// Serve is a thin convenience over (NewServer + Register + Run).
//
// Use it when you don't need to share the MCP server with anything else —
// 90% of integrations just want "load this spec, expose it on stdio".
// For multi-spec composition (registering two specs onto one server with
// different name prefixes) or custom server hooks, keep using [Register]
// directly: Serve is intentionally a single, opinionated entry point that
// does not expose every knob.
//
// Serve blocks until ctx is cancelled or the transport's peer disconnects.
func Serve(ctx context.Context, doc *Doc, opts ...ServeOption) error {
	cfg := &serveConfig{
		name:      "renseijin",
		version:   "0.0.0",
		transport: &mcp.StdioTransport{},
	}
	for _, o := range opts {
		o(cfg)
	}

	srv := mcp.NewServer(&mcp.Implementation{Name: cfg.name, Version: cfg.version}, nil)
	if err := Register(srv, doc, cfg.register...); err != nil {
		return fmt.Errorf("renseijin.Serve: %w", err)
	}
	return srv.Run(ctx, cfg.transport)
}

// Command renseijin loads an OpenAPI 3.x document and serves it as an MCP
// server. It is a thin wrapper around renseijin.Serve / renseijin.Register
// so the project is demoable without writing any Go.
//
// Usage:
//
//	renseijin --spec petstore.yaml
//	renseijin --spec petstore.yaml --transport http --addr :8080
//	renseijin --spec petstore.yaml --base-url https://sandbox.example.com/v1
//
// Auth: this binary does not load credentials of its own. If your upstream
// API needs a bearer token, prefer importing the library and supplying an
// *http.Client whose Transport adds the header; the CLI uses
// http.DefaultClient on purpose so it cannot be tricked into leaking
// secrets.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/junkd0g/renseijin"
)

func main() {
	var (
		spec      = flag.String("spec", "", "path to an OpenAPI 3.x document (required)")
		transport = flag.String("transport", "stdio", "transport: stdio | http")
		addr      = flag.String("addr", ":8080", "listen address (transport=http only)")
		baseURL   = flag.String("base-url", "", "override servers[].url from the spec")
		prefix    = flag.String("prefix", "", "prepend this string to every generated tool name")
		name      = flag.String("name", "renseijin", "MCP implementation name advertised on initialize")
		version   = flag.String("version", "0.0.0", "MCP implementation version advertised on initialize")
	)
	flag.Parse()

	if *spec == "" {
		fmt.Fprintln(os.Stderr, "renseijin: --spec is required")
		flag.Usage()
		os.Exit(2)
	}

	doc, err := renseijin.LoadFile(*spec)
	if err != nil {
		log.Fatalf("load spec: %v", err)
	}

	regOpts := []renseijin.Option{renseijin.WithHTTPClient(http.DefaultClient)}
	if *baseURL != "" {
		regOpts = append(regOpts, renseijin.WithBaseURL(*baseURL))
	}
	if *prefix != "" {
		regOpts = append(regOpts, renseijin.WithToolNamePrefix(*prefix))
	}

	// Cancel on SIGINT/SIGTERM so containerized deploys shut down cleanly
	// instead of leaking a child stdio process.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	switch *transport {
	case "stdio":
		err = renseijin.Serve(ctx, doc,
			renseijin.WithServerInfo(*name, *version),
			renseijin.WithRegisterOptions(regOpts...),
		)
	case "http":
		err = serveHTTP(ctx, doc, *addr, *name, *version, regOpts)
	default:
		log.Fatalf("renseijin: unknown transport %q (want stdio or http)", *transport)
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}

// serveHTTP wires the spec onto a streamable-HTTP MCP server.
//
// We build the server once and reuse it for every incoming session. The
// StreamableHTTPHandler factory is called per-request but always returns
// the same *mcp.Server — generated tools, their handlers, and the
// underlying *http.Client are immutable after Register returns, so sharing
// is safe.
func serveHTTP(ctx context.Context, doc *renseijin.Doc, addr, name, version string, regOpts []renseijin.Option) error {
	srv := mcp.NewServer(&mcp.Implementation{Name: name, Version: version}, nil)
	if err := renseijin.Register(srv, doc, regOpts...); err != nil {
		return fmt.Errorf("register: %w", err)
	}
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	httpSrv := &http.Server{Addr: addr, Handler: handler}

	shutdown := make(chan error, 1)
	go func() {
		<-ctx.Done()
		shutdown <- httpSrv.Shutdown(context.Background())
	}()

	log.Printf("renseijin: listening on %s", addr)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return <-shutdown
}

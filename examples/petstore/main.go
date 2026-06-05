// Petstore example: load examples/petstore/petstore.yaml, register every
// operation as an MCP tool on a stdio MCP server.
//
// Auth, if any, would be supplied via the *http.Client transport passed into
// openapi.Register — this example uses an unauthenticated client.
package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/junkd0g/renseijin/openapi"
)

func main() {
	doc, err := openapi.LoadFile("petstore.yaml")
	if err != nil {
		log.Fatalf("load spec: %v", err)
	}

	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "petstore",
		Version: "0.1.0",
	}, nil)

	if err := openapi.Register(srv, doc,
		openapi.WithHTTPClient(http.DefaultClient),
	); err != nil {
		log.Fatalf("register tools: %v", err)
	}

	ctx := context.Background()
	if err := srv.Run(ctx, &mcp.StdioTransport{}); err != nil {
		log.Fatalf("serve: %v", err)
	}
	_ = os.Stdout
}

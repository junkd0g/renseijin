// Package renseijin turns an OpenAPI 3.x document into MCP tools.
//
// The caller owns the *mcp.Server and the *http.Client; this package never
// holds credentials. Auth is supplied entirely through the http.Client's
// transport (e.g. an oauth2.Transport, a custom RoundTripper that signs
// requests, etc.).
package renseijin

import (
	"fmt"

	"github.com/getkin/kin-openapi/openapi3"
)

// Doc wraps a parsed OpenAPI v3 document. Construct one with [LoadFile],
// [LoadData], or [FromT] (when you already have an *openapi3.T).
type Doc struct {
	T *openapi3.T
}

// LoadFile parses an OpenAPI 3.x spec from a file on disk.
func LoadFile(path string) (*Doc, error) {
	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = true
	t, err := loader.LoadFromFile(path)
	if err != nil {
		return nil, fmt.Errorf("renseijin: load %s: %w", path, err)
	}
	return &Doc{T: t}, nil
}

// LoadData parses an OpenAPI 3.x spec from an in-memory byte slice.
func LoadData(data []byte) (*Doc, error) {
	loader := openapi3.NewLoader()
	t, err := loader.LoadFromData(data)
	if err != nil {
		return nil, fmt.Errorf("renseijin: load from data: %w", err)
	}
	return &Doc{T: t}, nil
}

// FromT wraps an already-parsed *openapi3.T.
func FromT(t *openapi3.T) *Doc {
	return &Doc{T: t}
}

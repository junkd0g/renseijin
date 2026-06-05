package openapi

import (
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Register adds one MCP tool to srv for each operation defined in doc.
//
// The caller owns srv: this function only calls srv.AddTool. It does not
// start, stop, or otherwise mutate the server. Auth is supplied through the
// http.Client passed via WithHTTPClient; this package never holds credentials.
func Register(srv *mcp.Server, doc *Doc, opts ...Option) error {
	if srv == nil {
		return fmt.Errorf("openapi.Register: nil *mcp.Server")
	}
	if doc == nil || doc.T == nil {
		return fmt.Errorf("openapi.Register: nil document")
	}
	cfg := newConfig(opts)

	baseURL := cfg.baseURL
	if baseURL == "" {
		baseURL = firstServerURL(doc)
	}

	for _, op := range collectOperations(doc.T) {
		name := op.toolName(cfg.namePrefix)
		tool := &mcp.Tool{
			Name:        name,
			Description: describeOperation(op),
			InputSchema: buildInputSchema(op),
		}
		srv.AddTool(tool, makeHandler(op, baseURL, cfg.httpClient))
	}
	return nil
}

func firstServerURL(doc *Doc) string {
	if doc == nil || doc.T == nil {
		return ""
	}
	for _, s := range doc.T.Servers {
		if s != nil && s.URL != "" {
			return s.URL
		}
	}
	return ""
}

// describeOperation builds the tool description shown to LLM clients. We
// prefer Summary; fall back to Description; finally to "METHOD path".
func describeOperation(op operation) string {
	var parts []string
	if s := strings.TrimSpace(op.op.Summary); s != "" {
		parts = append(parts, s)
	}
	if s := strings.TrimSpace(op.op.Description); s != "" && s != op.op.Summary {
		parts = append(parts, s)
	}
	if len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%s %s", op.method, op.pathTmpl))
	} else {
		parts = append(parts, fmt.Sprintf("(%s %s)", op.method, op.pathTmpl))
	}
	return strings.Join(parts, "\n")
}

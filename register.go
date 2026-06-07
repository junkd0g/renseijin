package renseijin

import (
	"fmt"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Register adds one MCP tool to srv for each operation defined in doc.
//
// The caller owns srv: this function only calls srv.AddTool. It does not
// start, stop, or otherwise mutate the server. Auth is supplied through the
// http.Client passed via WithHTTPClient; this package never holds credentials.
func Register(srv *mcp.Server, doc *Doc, opts ...Option) error {
	if srv == nil {
		return fmt.Errorf("renseijin.Register: nil *mcp.Server")
	}
	if doc == nil || doc.T == nil {
		return fmt.Errorf("renseijin.Register: nil document")
	}
	cfg := newConfig(opts)

	// WithBaseURL is treated as the literal final URL and skips spec-side
	// server-variable resolution. Anything else means we walk the spec's
	// first server and substitute {var} placeholders before mounting any
	// tools — failing at startup if a placeholder is unresolvable beats
	// failing on every tool call with a DNS error.
	baseURL := cfg.baseURL
	if baseURL == "" {
		resolved, err := resolveServerURL(doc, cfg.serverVariables)
		if err != nil {
			return fmt.Errorf("renseijin.Register: %w", err)
		}
		baseURL = resolved
	}

	for _, op := range collectOperations(doc.T) {
		name := op.toolName(cfg.namePrefix)
		tool := &mcp.Tool{
			Name:        name,
			Description: describeOperation(op),
			InputSchema: buildInputSchema(op),
		}
		srv.AddTool(tool, makeHandler(op, baseURL, cfg))
	}
	return nil
}

// resolveServerURL picks the first non-empty entry from doc.T.Servers and
// substitutes any {var} placeholders in its URL.
//
// Resolution order per placeholder:
//  1. caller-supplied override (WithServerVariables)
//  2. spec-side ServerVariable.Default
//
// Returns an error listing any placeholders that could not be resolved. We
// fail loudly rather than silently letting "{region}.example.com" reach the
// wire — every tool call would then fail with a confusing DNS error.
func resolveServerURL(doc *Doc, overrides map[string]string) (string, error) {
	if doc == nil || doc.T == nil {
		return "", nil
	}
	var srv *openapi3.Server
	for _, s := range doc.T.Servers {
		if s != nil && s.URL != "" {
			srv = s
			break
		}
	}
	if srv == nil {
		return "", nil
	}

	resolved := srv.URL
	var missing []string
	for _, name := range extractServerVarNames(srv.URL) {
		placeholder := "{" + name + "}"
		if v, ok := overrides[name]; ok {
			resolved = strings.ReplaceAll(resolved, placeholder, v)
			continue
		}
		if sv, ok := srv.Variables[name]; ok && sv != nil && sv.Default != "" {
			resolved = strings.ReplaceAll(resolved, placeholder, sv.Default)
			continue
		}
		missing = append(missing, name)
	}
	if len(missing) > 0 {
		return "", fmt.Errorf(
			"server URL %q has unresolved variable(s) %v — pass WithServerVariables or WithBaseURL",
			srv.URL, missing,
		)
	}
	return resolved, nil
}

// extractServerVarNames returns the placeholder names found in s, in order
// of appearance, deduped. We don't use a regex — server URLs are short and
// the OpenAPI grammar for placeholders is simple ("{" name "}").
func extractServerVarNames(s string) []string {
	var out []string
	seen := map[string]bool{}
	for {
		i := strings.Index(s, "{")
		if i < 0 {
			return out
		}
		s = s[i+1:]
		j := strings.Index(s, "}")
		if j < 0 {
			return out
		}
		name := s[:j]
		if name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
		s = s[j+1:]
	}
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

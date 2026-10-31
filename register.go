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
	if cfg.maxResponseBytes < 0 {
		return fmt.Errorf("renseijin.Register: max response bytes must be non-negative")
	}
	// Prepare the entire batch before mutating the caller's server.
	type preparedTool struct {
		tool    *mcp.Tool
		handler mcp.ToolHandler
	}
	var prepared []preparedTool
	names := map[string]string{}
	for _, op := range collectOperations(doc.T) {
		name := op.toolName(cfg.namePrefix)
		if prev, exists := names[name]; exists {
			return fmt.Errorf("renseijin.Register: duplicate tool name %q for %s and %s %s; assign unique operationIds", name, prev, op.method, op.pathTmpl)
		}
		names[name] = op.method + " " + op.pathTmpl
		baseURL := cfg.baseURL
		if baseURL == "" {
			var err error
			baseURL, err = resolveServers(op.servers, cfg.serverVariables)
			if err != nil {
				return fmt.Errorf("renseijin.Register: %s: %w", name, err)
			}
		}
		for _, ref := range op.parameters {
			if ref != nil && ref.Value != nil {
				if err := checkParameterEncoding(ref.Value); err != nil {
					return fmt.Errorf("renseijin.Register: %s: %w", name, err)
				}
			}
		}
		schema := buildInputSchema(op)
		validator, err := compileInputSchema(schema)
		if err != nil {
			return fmt.Errorf("renseijin.Register: %s input schema: %w", name, err)
		}
		op.inputValidator = validator
		tool := &mcp.Tool{
			Name:        name,
			Description: describeOperation(op),
			InputSchema: schema,
		}
		prepared = append(prepared, preparedTool{tool, makeHandler(op, baseURL, cfg)})
	}
	for _, p := range prepared {
		srv.AddTool(p.tool, p.handler)
	}
	return nil
}

// resolveServers picks the first non-empty entry from the selected servers and
// substitutes any {var} placeholders in its URL.
//
// Resolution order per placeholder:
//  1. caller-supplied override (WithServerVariables)
//  2. spec-side ServerVariable.Default
//
// Returns an error listing any placeholders that could not be resolved. We
// fail loudly rather than silently letting "{region}.example.com" reach the
// wire — every tool call would then fail with a confusing DNS error.
func resolveServers(servers openapi3.Servers, overrides map[string]string) (string, error) {
	var srv *openapi3.Server
	for _, s := range servers {
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
		if sv, ok := srv.Variables[name]; ok && sv != nil {
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

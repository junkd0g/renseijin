package renseijin

import (
	"net/http"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// operation is everything we need to build one MCP tool and dispatch one HTTP
// call. It is internal to the package — callers never see this type.
type operation struct {
	method         string
	pathTmpl       string              // e.g. "/pets/{petId}"
	op             *openapi3.Operation // op-level metadata
	parameters     openapi3.Parameters // path-level + op-level, deduped
	servers        openapi3.Servers
	contentType    string
	inputValidator *jsonschema.Schema
}

// httpMethods is the ordered set of HTTP methods kin-openapi exposes on
// PathItem. We iterate in this fixed order so tool registration is
// deterministic.
var httpMethods = []struct {
	name string
	get  func(*openapi3.PathItem) *openapi3.Operation
}{
	{http.MethodGet, func(p *openapi3.PathItem) *openapi3.Operation { return p.Get }},
	{http.MethodPut, func(p *openapi3.PathItem) *openapi3.Operation { return p.Put }},
	{http.MethodPost, func(p *openapi3.PathItem) *openapi3.Operation { return p.Post }},
	{http.MethodDelete, func(p *openapi3.PathItem) *openapi3.Operation { return p.Delete }},
	{http.MethodPatch, func(p *openapi3.PathItem) *openapi3.Operation { return p.Patch }},
	{http.MethodHead, func(p *openapi3.PathItem) *openapi3.Operation { return p.Head }},
	{http.MethodOptions, func(p *openapi3.PathItem) *openapi3.Operation { return p.Options }},
	{http.MethodTrace, func(p *openapi3.PathItem) *openapi3.Operation { return p.Trace }},
	{"CONNECT", func(p *openapi3.PathItem) *openapi3.Operation { return p.Connect }},
}

// collectOperations walks the spec's paths in a deterministic order and
// returns one operation per (path, method) that defines an op.
func collectOperations(t *openapi3.T) []operation {
	if t == nil || t.Paths == nil {
		return nil
	}
	m := t.Paths.Map()
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var out []operation
	for _, path := range keys {
		pi := m[path]
		if pi == nil {
			continue
		}
		for _, hm := range httpMethods {
			op := hm.get(pi)
			if op == nil {
				continue
			}
			servers := t.Servers
			if len(pi.Servers) > 0 {
				servers = pi.Servers
			}
			if op.Servers != nil && len(*op.Servers) > 0 {
				servers = *op.Servers
			}
			contentType := ""
			if op.RequestBody != nil && op.RequestBody.Value != nil {
				contentType = pickRequestContentType(op.RequestBody.Value)
			}
			out = append(out, operation{
				method:      hm.name,
				pathTmpl:    path,
				op:          op,
				parameters:  mergeParameters(pi.Parameters, op.Parameters),
				servers:     servers,
				contentType: contentType,
			})
		}
	}
	return out
}

// parameterNames keeps ordinary names intact and allocates aliases for names
// shared by different locations or reserved for the request body. All original
// names are reserved first, so generated aliases cannot hide another parameter.
func (op operation) parameterNames() map[*openapi3.Parameter]string {
	counts := map[string]int{}
	used := map[string]bool{}
	if op.op.RequestBody != nil {
		counts["body"]++
		used["body"] = true
	}
	for _, ref := range op.parameters {
		if ref != nil && ref.Value != nil {
			counts[ref.Value.Name]++
			used[ref.Value.Name] = true
		}
	}
	out := map[*openapi3.Parameter]string{}
	for _, ref := range op.parameters {
		if ref == nil || ref.Value == nil {
			continue
		}
		p := ref.Value
		name := p.Name
		if counts[name] > 1 {
			base := p.In + "." + p.Name
			name = base
			for used[name] {
				name += "_"
			}
			used[name] = true
		}
		out[p] = name
	}
	return out
}

func (op operation) requestContentType() string {
	if op.contentType != "" {
		return op.contentType
	}
	if op.op.RequestBody != nil {
		return pickRequestContentType(op.op.RequestBody.Value)
	}
	return "application/json"
}

// mergeParameters combines path-item-level and operation-level parameters.
// Op-level entries override path-item entries that share the same (name, in)
// per the OpenAPI spec.
func mergeParameters(pathLevel, opLevel openapi3.Parameters) openapi3.Parameters {
	type key struct{ name, in string }
	seen := map[key]bool{}
	out := make(openapi3.Parameters, 0, len(pathLevel)+len(opLevel))
	for _, p := range opLevel {
		if p == nil || p.Value == nil {
			continue
		}
		k := key{p.Value.Name, p.Value.In}
		seen[k] = true
		out = append(out, p)
	}
	for _, p := range pathLevel {
		if p == nil || p.Value == nil {
			continue
		}
		k := key{p.Value.Name, p.Value.In}
		if seen[k] {
			continue
		}
		out = append(out, p)
	}
	return out
}

// toolName picks a stable, MCP-legal tool name for an operation.
// Preference order: explicit operationId → "METHOD path" sanitized.
func (op operation) toolName(prefix string) string {
	base := op.op.OperationID
	if base == "" {
		base = strings.ToLower(op.method) + sanitize(op.pathTmpl)
	}
	return prefix + base
}

// sanitize maps "/pets/{petId}" → "_pets_petId" — safe for MCP tool names
// (which the SDK validates against a conservative pattern).
func sanitize(path string) string {
	var b strings.Builder
	for _, r := range path {
		switch r {
		case '/', '-', '.':
			b.WriteByte('_')
		case '{', '}':
			// drop braces; the variable name itself is kept
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

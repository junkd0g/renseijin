package openapi

import (
	"github.com/getkin/kin-openapi/openapi3"
)

// buildInputSchema produces the JSON Schema (as map[string]any) for one
// operation's MCP tool input. The top-level object has one property per
// non-body parameter (path/query/header/cookie) plus a "body" property when
// the operation declares a request body.
//
// We return map[string]any because the MCP SDK's Tool.InputSchema accepts
// "any value that JSON-marshals to valid JSON schema" — and we don't have a
// compile-time Go type to feed to the generic AddTool[In,Out].
func buildInputSchema(op operation) map[string]any {
	properties := map[string]any{}
	var required []string

	for _, pref := range op.parameters {
		if pref == nil || pref.Value == nil {
			continue
		}
		p := pref.Value
		s := schemaToMap(p.Schema)
		if s == nil {
			s = map[string]any{"type": "string"}
		}
		if p.Description != "" {
			s["description"] = p.Description
		}
		// Tag every non-body param with where it travels on the wire, so
		// the handler can route values back to path/query/header/cookie.
		s["x-in"] = p.In
		properties[p.Name] = s
		if p.Required {
			required = append(required, p.Name)
		}
	}

	if op.op.RequestBody != nil && op.op.RequestBody.Value != nil {
		rb := op.op.RequestBody.Value
		bodySchema, mediaType := preferredBodySchema(rb)
		if bodySchema == nil {
			bodySchema = map[string]any{}
		}
		if rb.Description != "" {
			bodySchema["description"] = rb.Description
		}
		if mediaType != "" {
			bodySchema["x-media-type"] = mediaType
		}
		properties["body"] = bodySchema
		if rb.Required {
			required = append(required, "body")
		}
	}

	out := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

// preferredBodySchema picks a media type's schema for the request body. We
// prefer application/json; otherwise we take whichever entry comes back
// first.
func preferredBodySchema(rb *openapi3.RequestBody) (map[string]any, string) {
	if rb == nil || rb.Content == nil {
		return nil, ""
	}
	if mt, ok := rb.Content["application/json"]; ok && mt != nil {
		return schemaToMap(mt.Schema), "application/json"
	}
	for name, mt := range rb.Content {
		if mt == nil {
			continue
		}
		return schemaToMap(mt.Schema), name
	}
	return nil, ""
}

// schemaToMap converts a kin-openapi SchemaRef into a plain JSON Schema map.
// It walks Properties/Items/AllOf/AnyOf/OneOf recursively.
//
// We deliberately render a minimal, lossy projection — enough for an LLM to
// understand the shape, not a faithful 1-to-1 of every OpenAPI dialect quirk.
func schemaToMap(ref *openapi3.SchemaRef) map[string]any {
	if ref == nil || ref.Value == nil {
		return nil
	}
	s := ref.Value
	out := map[string]any{}

	if s.Type != nil && len(*s.Type) > 0 {
		if len(*s.Type) == 1 {
			out["type"] = (*s.Type)[0]
		} else {
			out["type"] = s.Type.Slice()
		}
	}
	if s.Format != "" {
		out["format"] = s.Format
	}
	if s.Description != "" {
		out["description"] = s.Description
	}
	if s.Title != "" {
		out["title"] = s.Title
	}
	if len(s.Enum) > 0 {
		out["enum"] = s.Enum
	}
	if s.Default != nil {
		out["default"] = s.Default
	}
	if s.Min != nil {
		out["minimum"] = *s.Min
	}
	if s.Max != nil {
		out["maximum"] = *s.Max
	}
	if s.MinLength != 0 {
		out["minLength"] = s.MinLength
	}
	if s.MaxLength != nil {
		out["maxLength"] = *s.MaxLength
	}
	if s.Pattern != "" {
		out["pattern"] = s.Pattern
	}
	if s.Items != nil {
		out["items"] = schemaToMap(s.Items)
	}
	if len(s.Properties) > 0 {
		props := map[string]any{}
		for k, v := range s.Properties {
			props[k] = schemaToMap(v)
		}
		out["properties"] = props
	}
	if len(s.Required) > 0 {
		out["required"] = append([]string(nil), s.Required...)
	}
	if refs := s.AllOf; len(refs) > 0 {
		out["allOf"] = schemaRefSlice(refs)
	}
	if refs := s.AnyOf; len(refs) > 0 {
		out["anyOf"] = schemaRefSlice(refs)
	}
	if refs := s.OneOf; len(refs) > 0 {
		out["oneOf"] = schemaRefSlice(refs)
	}
	return out
}

func schemaRefSlice(refs openapi3.SchemaRefs) []any {
	out := make([]any, 0, len(refs))
	for _, r := range refs {
		out = append(out, schemaToMap(r))
	}
	return out
}

package renseijin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/santhosh-tekuri/jsonschema/v6"
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
	c := &schemaConverter{active: map[*openapi3.Schema]string{}}
	names := op.parameterNames()

	for _, pref := range op.parameters {
		if pref == nil || pref.Value == nil {
			continue
		}
		p := pref.Value
		name := names[p]
		s := c.convert(p.Schema, "#/properties/"+pointerToken(name), false)
		if s == nil {
			s = map[string]any{"type": "string"}
		}
		if p.Description != "" {
			s["description"] = p.Description
		}
		// Tag every non-body param with where it travels on the wire, so
		// the handler can route values back to path/query/header/cookie.
		s["x-in"] = p.In
		s["x-name"] = p.Name
		properties[name] = s
		if p.Required {
			required = append(required, name)
		}
	}

	if op.op.RequestBody != nil && op.op.RequestBody.Value != nil {
		rb := op.op.RequestBody.Value
		mediaType := op.requestContentType()
		var bodySchema map[string]any
		if mt := rb.Content[mediaType]; mt != nil {
			bodySchema = c.convert(mt.Schema, "#/properties/body", mediaType == "multipart/form-data")
		}
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
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

// preferredBodySchema picks a media type's schema for the request body. We
// prefer application/json; otherwise use a stable supported-media preference.
func preferredBodySchema(rb *openapi3.RequestBody) (map[string]any, string) {
	if rb == nil || rb.Content == nil {
		return nil, ""
	}
	name := pickRequestContentType(rb)
	if mt := rb.Content[name]; mt != nil {
		return schemaToMap(mt.Schema), name
	}
	return nil, ""
}

// schemaToMap converts a schema graph into JSON Schema. Back edges use local
// JSON Pointers, so recursive schemas remain finite and validate recursively.
func schemaToMap(ref *openapi3.SchemaRef) map[string]any {
	c := &schemaConverter{active: map[*openapi3.Schema]string{}}
	return c.convert(ref, "#", false)
}

type schemaConverter struct{ active map[*openapi3.Schema]string }

func pointerToken(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}

func (c *schemaConverter) convert(ref *openapi3.SchemaRef, path string, multipart bool) map[string]any {
	if ref == nil || ref.Value == nil {
		return nil
	}
	s := ref.Value
	if target, ok := c.active[s]; ok {
		return map[string]any{"$ref": target}
	}
	c.active[s] = path
	defer delete(c.active, s)
	if multipart && s.Format == "binary" && s.Type.Is("string") {
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"filename":       map[string]any{"type": "string", "minLength": 1},
				"content_base64": map[string]any{"type": "string", "contentEncoding": "base64"},
			},
			"required":             []string{"filename", "content_base64"},
			"additionalProperties": false,
		}
	}
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
	if s.Nullable && s.Type != nil && !s.Type.IncludesNull() {
		out["type"] = append(append([]string(nil), s.Type.Slice()...), "null")
		if len(s.Enum) > 0 {
			out["enum"] = append(append([]any(nil), s.Enum...), nil)
		}
	}
	if s.Min != nil {
		out["minimum"] = *s.Min
	}
	if s.Max != nil {
		out["maximum"] = *s.Max
	}
	if s.ExclusiveMin.Value != nil {
		out["exclusiveMinimum"] = *s.ExclusiveMin.Value
	} else if s.ExclusiveMin.IsTrue() && s.Min != nil {
		out["exclusiveMinimum"] = *s.Min
		delete(out, "minimum")
	}
	if s.ExclusiveMax.Value != nil {
		out["exclusiveMaximum"] = *s.ExclusiveMax.Value
	} else if s.ExclusiveMax.IsTrue() && s.Max != nil {
		out["exclusiveMaximum"] = *s.Max
		delete(out, "maximum")
	}
	if s.MultipleOf != nil {
		out["multipleOf"] = *s.MultipleOf
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
		out["items"] = c.convert(s.Items, path+"/items", multipart)
	}
	if s.MinItems != 0 {
		out["minItems"] = s.MinItems
	}
	if s.MaxItems != nil {
		out["maxItems"] = *s.MaxItems
	}
	if s.UniqueItems {
		out["uniqueItems"] = true
	}
	if s.MinProps != 0 {
		out["minProperties"] = s.MinProps
	}
	if s.MaxProps != nil {
		out["maxProperties"] = *s.MaxProps
	}
	if s.AdditionalProperties.Has != nil {
		out["additionalProperties"] = *s.AdditionalProperties.Has
	} else if s.AdditionalProperties.Schema != nil {
		out["additionalProperties"] = c.convert(s.AdditionalProperties.Schema, path+"/additionalProperties", multipart)
	}
	if len(s.Properties) > 0 {
		props := map[string]any{}
		keys := make([]string, 0, len(s.Properties))
		for k := range s.Properties {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := s.Properties[k]
			if v != nil && v.Value != nil && v.Value.ReadOnly {
				continue
			}
			props[k] = c.convert(v, path+"/properties/"+pointerToken(k), multipart)
		}
		out["properties"] = props
	}
	if len(s.Required) > 0 {
		var required []string
		for _, name := range s.Required {
			p := s.Properties[name]
			if p != nil && p.Value != nil && p.Value.ReadOnly {
				continue
			}
			required = append(required, name)
		}
		if len(required) > 0 {
			out["required"] = required
		}
	}
	if refs := s.AllOf; len(refs) > 0 {
		out["allOf"] = c.slice(refs, path+"/allOf", multipart)
	}
	if refs := s.AnyOf; len(refs) > 0 {
		out["anyOf"] = c.slice(refs, path+"/anyOf", multipart)
	}
	if refs := s.OneOf; len(refs) > 0 {
		out["oneOf"] = c.slice(refs, path+"/oneOf", multipart)
	}
	if s.Not != nil {
		out["not"] = c.convert(s.Not, path+"/not", multipart)
	}
	if s.Const != nil {
		out["const"] = s.Const
	}
	return out
}

func (c *schemaConverter) slice(refs openapi3.SchemaRefs, path string, multipart bool) []any {
	out := make([]any, 0, len(refs))
	for i, r := range refs {
		out = append(out, c.convert(r, fmt.Sprintf("%s/%d", path, i), multipart))
	}
	return out
}

// Compile the exact advertised schema once. JSON normalization also converts
// typed Go slices into the JSON values accepted by the validator.
func compileInputSchema(schema map[string]any) (*jsonschema.Schema, error) {
	b, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.UseLoader(jsonschema.SchemeURLLoader{}) // Generated references must be local.
	const location = "https://renseijin.invalid/input.json"
	if err := c.AddResource(location, v); err != nil {
		return nil, err
	}
	return c.Compile(location)
}

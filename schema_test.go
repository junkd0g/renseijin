package renseijin

import (
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const paramsSpec = `openapi: 3.0.3
info:
  title: p
  version: "1.0"
paths:
  /a/{p1}:
    get:
      operationId: getA
      parameters:
        - name: p1
          in: path
          required: true
          description: the id
          schema: {type: string}
        - name: q1
          in: query
          schema: {type: integer}
        - name: h1
          in: header
          required: true
          schema: {type: string}
        - name: c1
          in: cookie
          schema: {type: string}
      responses: {"200": {description: ok}}
`

func TestBuildInputSchema_NonBodyParams_TaggedWithXIn(t *testing.T) {
	ops := collectOperations(loadT(t, paramsSpec))
	require.Len(t, ops, 1)

	s := buildInputSchema(ops[0])
	assert.Equal(t, "object", s["type"])

	props, ok := s["properties"].(map[string]any)
	require.True(t, ok, "properties must be map[string]any")

	wantIn := map[string]string{"p1": "path", "q1": "query", "h1": "header", "c1": "cookie"}
	for name, in := range wantIn {
		p, ok := props[name].(map[string]any)
		require.Truef(t, ok, "missing property %q", name)
		assert.Equalf(t, in, p["x-in"], "x-in for %q", name)
	}

	// description is copied from the parameter (not from its schema).
	p1 := props["p1"].(map[string]any)
	assert.Equal(t, "the id", p1["description"])
}

func TestBuildInputSchema_RequiredAggregatesParamsAndBody(t *testing.T) {
	ops := collectOperations(loadT(t, paramsSpec))
	s := buildInputSchema(ops[0])

	req, ok := s["required"].([]string)
	require.True(t, ok, "required must be []string")
	assert.ElementsMatch(t, []string{"p1", "h1"}, req)
}

func TestBuildInputSchema_NoSchemaParam_DefaultsToString(t *testing.T) {
	// White-box: construct an operation with a parameter whose Schema is nil.
	op := operation{
		op: &openapi3.Operation{},
		parameters: openapi3.Parameters{
			&openapi3.ParameterRef{Value: &openapi3.Parameter{
				Name: "noSchema", In: openapi3.ParameterInQuery,
			}},
		},
	}
	s := buildInputSchema(op)
	props := s["properties"].(map[string]any)
	p, ok := props["noSchema"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "string", p["type"])
	assert.Equal(t, "query", p["x-in"])
}

func TestBuildInputSchema_NoRequiredParams_NoRequiredKey(t *testing.T) {
	op := operation{
		op: &openapi3.Operation{},
		parameters: openapi3.Parameters{
			&openapi3.ParameterRef{Value: &openapi3.Parameter{
				Name: "limit", In: openapi3.ParameterInQuery,
				Schema: &openapi3.SchemaRef{Value: openapi3.NewIntegerSchema()},
			}},
		},
	}
	s := buildInputSchema(op)
	_, has := s["required"]
	assert.False(t, has, "required key should be absent when nothing is required")
}

const jsonBodySpec = `openapi: 3.0.3
info:
  title: b
  version: "1.0"
paths:
  /a:
    post:
      operationId: postA
      requestBody:
        required: true
        description: the body
        content:
          application/json:
            schema:
              type: object
              properties:
                name: {type: string}
              required: [name]
      responses: {"201": {description: ok}}
`

func TestBuildInputSchema_RequestBody_JSON(t *testing.T) {
	ops := collectOperations(loadT(t, jsonBodySpec))
	s := buildInputSchema(ops[0])

	props := s["properties"].(map[string]any)
	body, ok := props["body"].(map[string]any)
	require.True(t, ok, "body property must be present")

	assert.Equal(t, "application/json", body["x-media-type"])
	assert.Equal(t, "the body", body["description"], "RequestBody.Description must overwrite the schema description")

	req, _ := s["required"].([]string)
	assert.Contains(t, req, "body")
}

const xmlBodySpec = `openapi: 3.0.3
info:
  title: b
  version: "1.0"
paths:
  /a:
    post:
      operationId: postA
      requestBody:
        content:
          application/xml:
            schema: {type: string}
      responses: {"201": {description: ok}}
`

func TestPreferredBodySchema_FallsBackToNonJSONMediaType(t *testing.T) {
	ops := collectOperations(loadT(t, xmlBodySpec))
	s := buildInputSchema(ops[0])
	body := s["properties"].(map[string]any)["body"].(map[string]any)
	assert.Equal(t, "application/xml", body["x-media-type"])
}

func TestPreferredBodySchema_PrefersJSONOverOther(t *testing.T) {
	rb := &openapi3.RequestBody{
		Content: openapi3.Content{
			"application/xml":  &openapi3.MediaType{Schema: &openapi3.SchemaRef{Value: openapi3.NewStringSchema()}},
			"application/json": &openapi3.MediaType{Schema: &openapi3.SchemaRef{Value: openapi3.NewStringSchema()}},
		},
	}
	_, mt := preferredBodySchema(rb)
	assert.Equal(t, "application/json", mt)
}

func TestPreferredBodySchema_NilSafe(t *testing.T) {
	body, mt := preferredBodySchema(nil)
	assert.Nil(t, body)
	assert.Equal(t, "", mt)

	body, mt = preferredBodySchema(&openapi3.RequestBody{})
	assert.Nil(t, body)
	assert.Equal(t, "", mt)
}

func TestSchemaToMap_NilSafe(t *testing.T) {
	assert.Nil(t, schemaToMap(nil))
	assert.Nil(t, schemaToMap(&openapi3.SchemaRef{}))
}

const schemaFeaturesSpec = `openapi: 3.0.3
info:
  title: s
  version: "1.0"
paths:
  /a:
    post:
      operationId: postA
      requestBody:
        content:
          application/json:
            schema:
              type: object
              title: Body
              description: the body
              properties:
                name:
                  type: string
                  minLength: 1
                  maxLength: 5
                  pattern: "^[a-z]+$"
                age:
                  type: integer
                  format: int32
                  minimum: 0
                  maximum: 120
                  default: 0
                tag:
                  type: string
                  enum: [a, b, c]
                items:
                  type: array
                  items: {type: string}
                kind:
                  oneOf:
                    - {type: string}
                    - {type: integer}
                either:
                  anyOf:
                    - {type: string}
                    - {type: integer}
                composed:
                  allOf:
                    - {type: object, properties: {x: {type: string}}}
              required: [name]
      responses: {"201": {description: ok}}
`

func TestSchemaToMap_BroadFeatureCoverage(t *testing.T) {
	ops := collectOperations(loadT(t, schemaFeaturesSpec))
	body := buildInputSchema(ops[0])["properties"].(map[string]any)["body"].(map[string]any)

	assert.Equal(t, "object", body["type"])
	assert.Equal(t, "Body", body["title"])
	assert.Equal(t, "the body", body["description"])

	props := body["properties"].(map[string]any)

	name := props["name"].(map[string]any)
	assert.Equal(t, "string", name["type"])
	assert.Equal(t, "^[a-z]+$", name["pattern"])
	assert.Contains(t, name, "minLength")
	assert.Contains(t, name, "maxLength")

	age := props["age"].(map[string]any)
	assert.Equal(t, "integer", age["type"])
	assert.Equal(t, "int32", age["format"])
	assert.Contains(t, age, "minimum")
	assert.Contains(t, age, "maximum")
	assert.Contains(t, age, "default")

	tag := props["tag"].(map[string]any)
	enum, ok := tag["enum"].([]any)
	require.True(t, ok, "enum must be []any (the kin-openapi field is []interface{})")
	assert.Len(t, enum, 3)

	items := props["items"].(map[string]any)
	assert.Equal(t, "array", items["type"])
	itemsItems := items["items"].(map[string]any)
	assert.Equal(t, "string", itemsItems["type"])

	kind := props["kind"].(map[string]any)
	oneOf, ok := kind["oneOf"].([]any)
	require.True(t, ok)
	assert.Len(t, oneOf, 2)

	either := props["either"].(map[string]any)
	anyOf, ok := either["anyOf"].([]any)
	require.True(t, ok)
	assert.Len(t, anyOf, 2)

	composed := props["composed"].(map[string]any)
	allOf, ok := composed["allOf"].([]any)
	require.True(t, ok)
	assert.Len(t, allOf, 1)

	bodyReq, ok := body["required"].([]string)
	require.True(t, ok)
	assert.Equal(t, []string{"name"}, bodyReq)
}

func TestSchemaToMap_MultiTypeArrayIsExposedAsSlice(t *testing.T) {
	// OpenAPI 3.1 allows `type: [string, "null"]`. Verify the projection keeps
	// the slice form rather than collapsing to a single string.
	s := openapi3.NewSchema()
	s.Type = &openapi3.Types{"string", "null"}
	out := schemaToMap(&openapi3.SchemaRef{Value: s})
	require.NotNil(t, out)
	slice, ok := out["type"].([]string)
	require.Truef(t, ok, "expected []string for multi-type, got %T", out["type"])
	assert.Equal(t, []string{"string", "null"}, slice)
}

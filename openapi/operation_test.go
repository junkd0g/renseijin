package openapi

import (
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// loadT is a small helper for white-box tests: parse a YAML spec into
// *openapi3.T so internal helpers can be exercised against a real document.
func loadT(t *testing.T, spec string) *openapi3.T {
	t.Helper()
	d, err := LoadData([]byte(spec))
	require.NoError(t, err, "LoadData")
	require.NotNil(t, d)
	require.NotNil(t, d.T)
	return d.T
}

const allMethodsSpec = `openapi: 3.0.3
info:
  title: m
  version: "1.0"
paths:
  /a:
    get:
      operationId: getA
      responses: {"200": {description: ok}}
    put:
      operationId: putA
      responses: {"200": {description: ok}}
    post:
      operationId: postA
      responses: {"200": {description: ok}}
    delete:
      operationId: delA
      responses: {"200": {description: ok}}
    patch:
      operationId: patchA
      responses: {"200": {description: ok}}
    head:
      operationId: headA
      responses: {"200": {description: ok}}
    options:
      operationId: optsA
      responses: {"200": {description: ok}}
  /b:
    get:
      operationId: getB
      responses: {"200": {description: ok}}
`

func TestCollectOperations_NilSafe(t *testing.T) {
	assert.Nil(t, collectOperations(nil))
	assert.Nil(t, collectOperations(&openapi3.T{}))
}

func TestCollectOperations_DeterministicMethodAndPathOrder(t *testing.T) {
	ops := collectOperations(loadT(t, allMethodsSpec))

	// paths sorted alphabetically (/a then /b); within a path, fixed method
	// order (GET, PUT, POST, DELETE, PATCH, HEAD, OPTIONS, TRACE, CONNECT).
	want := []string{
		"getA", "putA", "postA", "delA", "patchA", "headA", "optsA",
		"getB",
	}
	got := make([]string, 0, len(ops))
	for _, op := range ops {
		got = append(got, op.op.OperationID)
	}
	assert.Equal(t, want, got)
}

const mergeParamsSpec = `openapi: 3.0.3
info:
  title: m
  version: "1.0"
paths:
  /x:
    parameters:
      - name: shared
        in: query
        required: true
        description: path-level
        schema: {type: string}
      - name: pathOnly
        in: query
        schema: {type: string}
    get:
      operationId: getX
      parameters:
        - name: shared
          in: query
          required: false
          description: op-level wins
          schema: {type: string}
        - name: opOnly
          in: query
          schema: {type: string}
      responses: {"200": {description: ok}}
`

func TestMergeParameters_OpLevelOverridesPathLevel(t *testing.T) {
	ops := collectOperations(loadT(t, mergeParamsSpec))
	require.Len(t, ops, 1)

	got := map[string]*openapi3.Parameter{}
	for _, pref := range ops[0].parameters {
		require.NotNil(t, pref)
		require.NotNil(t, pref.Value)
		got[pref.Value.Name] = pref.Value
	}

	require.Contains(t, got, "shared", "shared param must be present")
	assert.False(t, got["shared"].Required, "op-level required=false must win over path-level required=true")
	assert.Equal(t, "op-level wins", got["shared"].Description)

	assert.Contains(t, got, "pathOnly", "path-only param must be preserved")
	assert.Contains(t, got, "opOnly", "op-only param must be preserved")
}

func TestMergeParameters_NilEntriesIgnored(t *testing.T) {
	pathLevel := openapi3.Parameters{
		nil,
		&openapi3.ParameterRef{},
		&openapi3.ParameterRef{Value: &openapi3.Parameter{Name: "kept", In: "query"}},
	}
	opLevel := openapi3.Parameters{nil}

	out := mergeParameters(pathLevel, opLevel)
	require.Len(t, out, 1)
	assert.Equal(t, "kept", out[0].Value.Name)
}

func TestToolName_PrefersOperationID(t *testing.T) {
	op := operation{op: &openapi3.Operation{OperationID: "doThing"}, method: "GET", pathTmpl: "/a"}
	assert.Equal(t, "doThing", op.toolName(""))
	assert.Equal(t, "pfx_doThing", op.toolName("pfx_"))
}

func TestToolName_FallbackFromMethodAndPath(t *testing.T) {
	op := operation{op: &openapi3.Operation{}, method: "GET", pathTmpl: "/pets/{petId}/likes-count"}
	assert.Equal(t, "get_pets_petId_likes_count", op.toolName(""))
	assert.Equal(t, "x_get_pets_petId_likes_count", op.toolName("x_"))
}

func TestSanitize(t *testing.T) {
	cases := map[string]string{
		"/pets/{petId}":      "_pets_petId",
		"/a-b.c/{x}":         "_a_b_c_x",
		"/":                  "_",
		"":                   "",
		"plain":              "plain",
		"/users/{id}/orders": "_users_id_orders",
	}
	for in, want := range cases {
		assert.Equal(t, want, sanitize(in), "sanitize(%q)", in)
	}
}

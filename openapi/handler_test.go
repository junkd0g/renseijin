package openapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const handlerSpec = `openapi: 3.0.3
info:
  title: h
  version: "1.0"
paths:
  /pets/{petId}:
    get:
      operationId: getPet
      parameters:
        - {name: petId,   in: path,   required: true, schema: {type: string}}
        - {name: verbose, in: query,                  schema: {type: boolean}}
        - {name: X-Trace, in: header,                 schema: {type: string}}
        - {name: sid,     in: cookie,                 schema: {type: string}}
      responses: {"200": {description: ok}}
  /pets:
    post:
      operationId: createPet
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              properties:
                name: {type: string}
              required: [name]
      responses: {"201": {description: created}}
  /things:
    get:
      operationId: thingsList
      parameters:
        - {name: must, in: query, required: true, schema: {type: string}}
      responses: {"200": {description: ok}}
`

func loadOp(t *testing.T, name string) operation {
	t.Helper()
	ops := collectOperations(loadT(t, handlerSpec))
	for _, op := range ops {
		if op.op.OperationID == name {
			return op
		}
	}
	t.Fatalf("op %q not found in test fixture", name)
	return operation{}
}

func TestBuildHTTPRequest_PathSubstitution(t *testing.T) {
	op := loadOp(t, "getPet")
	req, err := buildHTTPRequest(context.Background(), op, "https://api.example/v1", map[string]any{
		"petId": "abc-123",
	})
	require.NoError(t, err)
	assert.Equal(t, "https://api.example/v1/pets/abc-123", req.URL.String())
}

func TestBuildHTTPRequest_PathParamIsURLEscaped(t *testing.T) {
	op := loadOp(t, "getPet")
	req, err := buildHTTPRequest(context.Background(), op, "https://api.example/v1", map[string]any{
		"petId": "a b/c",
	})
	require.NoError(t, err)
	// "a b/c" must be percent-encoded into the path; in particular the '/'
	// must not silently introduce a new path segment.
	assert.Contains(t, req.URL.EscapedPath(), "a%20b%2Fc")
}

func TestBuildHTTPRequest_TrimsTrailingSlashFromBaseURL(t *testing.T) {
	op := loadOp(t, "getPet")
	req, err := buildHTTPRequest(context.Background(), op, "https://api.example/v1/", map[string]any{
		"petId": "1",
	})
	require.NoError(t, err)
	assert.Equal(t, "https://api.example/v1/pets/1", req.URL.String())
}

func TestBuildHTTPRequest_QueryHeaderCookie(t *testing.T) {
	op := loadOp(t, "getPet")
	req, err := buildHTTPRequest(context.Background(), op, "https://api.example/v1", map[string]any{
		"petId":   "42",
		"verbose": true,
		"X-Trace": "abc",
		"sid":     "xyz",
	})
	require.NoError(t, err)

	assert.Equal(t, "true", req.URL.Query().Get("verbose"))
	assert.Equal(t, "abc", req.Header.Get("X-Trace"))

	c, err := req.Cookie("sid")
	require.NoError(t, err)
	assert.Equal(t, "xyz", c.Value)
}

func TestBuildHTTPRequest_DefaultAcceptHeader(t *testing.T) {
	op := loadOp(t, "getPet")
	req, err := buildHTTPRequest(context.Background(), op, "https://x", map[string]any{"petId": "1"})
	require.NoError(t, err)
	assert.Equal(t, "application/json", req.Header.Get("Accept"))
}

func TestBuildHTTPRequest_MissingRequiredParamErrors(t *testing.T) {
	op := loadOp(t, "thingsList")
	_, err := buildHTTPRequest(context.Background(), op, "https://x", map[string]any{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `missing required parameter "must"`)
}

func TestBuildHTTPRequest_OptionalParamsAreOmittedSilently(t *testing.T) {
	op := loadOp(t, "getPet")
	req, err := buildHTTPRequest(context.Background(), op, "https://x", map[string]any{"petId": "1"})
	require.NoError(t, err)
	assert.Empty(t, req.URL.RawQuery)
	assert.Empty(t, req.Header.Get("X-Trace"))
	_, cookieErr := req.Cookie("sid")
	assert.Error(t, cookieErr, "no cookie should have been added")
}

func TestBuildHTTPRequest_BodyIsJSONMarshaledAndContentTypeSet(t *testing.T) {
	op := loadOp(t, "createPet")
	req, err := buildHTTPRequest(context.Background(), op, "https://x", map[string]any{
		"body": map[string]any{"name": "rex"},
	})
	require.NoError(t, err)
	assert.Equal(t, "application/json", req.Header.Get("Content-Type"))

	body, readErr := io.ReadAll(req.Body)
	require.NoError(t, readErr)
	assert.JSONEq(t, `{"name":"rex"}`, string(body))
}

func TestBuildHTTPRequest_NoBodyWhenOperationDeclaresNone(t *testing.T) {
	op := loadOp(t, "getPet")
	req, err := buildHTTPRequest(context.Background(), op, "https://x", map[string]any{
		"petId": "1",
		"body":  map[string]any{"ignored": true}, // operation has no requestBody
	})
	require.NoError(t, err)
	// http.NewRequestWithContext sets req.Body to http.NoBody when the body
	// arg is nil; reading should yield 0 bytes either way.
	if req.Body != nil {
		buf, _ := io.ReadAll(req.Body)
		assert.Empty(t, buf)
	}
	assert.Empty(t, req.Header.Get("Content-Type"))
}

func TestStringify(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"string", "abc", "abc"},
		{"int-valued float", float64(42), "42"},
		{"negative int-valued float", float64(-3), "-3"},
		{"non-integer float", float64(42.5), "42.5"},
		{"true", true, "true"},
		{"false", false, "false"},
		{"nil", nil, ""},
		{"slice falls through to JSON", []any{1.0, 2.0}, "[1,2]"},
		{"map falls through to JSON", map[string]any{"a": "b"}, `{"a":"b"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, stringify(c.in))
		})
	}
}

func TestPickRequestContentType_NilRequestBody(t *testing.T) {
	assert.Equal(t, "application/json", pickRequestContentType(nil))
}

func TestPickRequestContentType_NilContent(t *testing.T) {
	assert.Equal(t, "application/json", pickRequestContentType(&openapi3.RequestBody{}))
}

func TestPickRequestContentType_PrefersJSON(t *testing.T) {
	rb := &openapi3.RequestBody{
		Content: openapi3.Content{
			"application/xml":  &openapi3.MediaType{},
			"application/json": &openapi3.MediaType{},
		},
	}
	assert.Equal(t, "application/json", pickRequestContentType(rb))
}

func TestPickRequestContentType_FallsBackToOnlyMediaType(t *testing.T) {
	rb := &openapi3.RequestBody{
		Content: openapi3.Content{"application/xml": &openapi3.MediaType{}},
	}
	assert.Equal(t, "application/xml", pickRequestContentType(rb))
}

// callRaw invokes a tool handler directly with raw JSON arguments, matching
// the shape the MCP SDK delivers on the server side.
func callRaw(t *testing.T, h mcp.ToolHandler, args any) *mcp.CallToolResult {
	t.Helper()
	raw, err := json.Marshal(args)
	require.NoError(t, err)
	res, err := h(context.Background(), &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{Arguments: raw},
	})
	require.NoError(t, err, "handler must never return a transport-level error; failures go in the result")
	require.NotNil(t, res)
	return res
}

func textOf(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	require.NotEmpty(t, res.Content)
	tc, ok := res.Content[0].(*mcp.TextContent)
	require.Truef(t, ok, "want *mcp.TextContent, got %T", res.Content[0])
	return tc.Text
}

func TestMakeHandler_ForwardsRequestAndFormatsSuccess(t *testing.T) {
	var seen *http.Request
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":7,"name":"rex"}`)
	}))
	t.Cleanup(backend.Close)

	op := loadOp(t, "getPet")
	h := makeHandler(op, backend.URL, backend.Client())

	res := callRaw(t, h, map[string]any{"petId": "7"})

	assert.False(t, res.IsError)
	assert.Equal(t, "/pets/7", seen.URL.Path)
	assert.Equal(t, http.MethodGet, seen.Method)

	text := textOf(t, res)
	assert.Contains(t, text, "HTTP 200")
	assert.Contains(t, text, `"name":"rex"`)
}

func TestMakeHandler_4xxMarksResultAsError(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	t.Cleanup(backend.Close)

	op := loadOp(t, "getPet")
	h := makeHandler(op, backend.URL, backend.Client())

	res := callRaw(t, h, map[string]any{"petId": "x"})

	assert.True(t, res.IsError, "≥400 status must surface as IsError")
	assert.Contains(t, textOf(t, res), "HTTP 404")
}

func TestMakeHandler_InvalidJSONArgsErrorsCleanly(t *testing.T) {
	op := loadOp(t, "getPet")
	h := makeHandler(op, "http://example.invalid", http.DefaultClient)

	res, err := h(context.Background(), &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{Arguments: json.RawMessage(`{not json`)},
	})
	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Contains(t, textOf(t, res), "invalid JSON arguments")
}

func TestMakeHandler_NoArguments_StillRunsForOperationsWithoutRequiredParams(t *testing.T) {
	var hit bool
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(backend.Close)

	// /pets POST has a required body — verify a missing-required path:
	op := loadOp(t, "createPet")
	h := makeHandler(op, backend.URL, backend.Client())

	res, err := h(context.Background(), &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{Arguments: nil},
	})
	require.NoError(t, err)
	// createPet's body is required at the parameter-validation layer of
	// buildInputSchema, but the handler treats a missing body as "no body
	// sent" and still issues the request (the backend may then reject).
	// We just need the backend to have been called — i.e. no error path.
	assert.False(t, res.IsError)
	assert.True(t, hit)
}

type erroringTransport struct{ err error }

func (e *erroringTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, e.err }

func TestMakeHandler_TransportErrorSurfacesAsToolError(t *testing.T) {
	op := loadOp(t, "getPet")
	cli := &http.Client{Transport: &erroringTransport{err: errors.New("boom")}}
	h := makeHandler(op, "http://example.invalid", cli)

	res := callRaw(t, h, map[string]any{"petId": "1"})

	assert.True(t, res.IsError)
	assert.Contains(t, textOf(t, res), "boom")
}

func TestMakeHandler_AuthLivesInCallerTransport(t *testing.T) {
	// The library promises never to handle credentials itself; instead any
	// auth headers the caller adds via Transport must land on the wire.
	var seen *http.Request
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(backend.Close)

	cli := &http.Client{Transport: &authTransport{
		token: "secret",
		base:  backend.Client().Transport,
	}}

	op := loadOp(t, "getPet")
	h := makeHandler(op, backend.URL, cli)

	res := callRaw(t, h, map[string]any{"petId": "1"})
	require.False(t, res.IsError)
	require.NotNil(t, seen)
	assert.Equal(t, "Bearer secret", seen.Header.Get("Authorization"))
}

type authTransport struct {
	token string
	base  http.RoundTripper
}

func (a *authTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+a.token)
	if a.base == nil {
		return http.DefaultTransport.RoundTrip(r)
	}
	return a.base.RoundTrip(r)
}

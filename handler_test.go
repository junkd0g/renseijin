package renseijin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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
	h := makeHandler(op, backend.URL, &config{httpClient: backend.Client()})

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
	h := makeHandler(op, backend.URL, &config{httpClient: backend.Client()})

	res := callRaw(t, h, map[string]any{"petId": "x"})

	assert.True(t, res.IsError, "≥400 status must surface as IsError")
	assert.Contains(t, textOf(t, res), "HTTP 404")
}

func TestMakeHandler_InvalidJSONArgsErrorsCleanly(t *testing.T) {
	op := loadOp(t, "getPet")
	h := makeHandler(op, "http://example.invalid", &config{httpClient: http.DefaultClient})

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
	h := makeHandler(op, backend.URL, &config{httpClient: backend.Client()})

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
	h := makeHandler(op, "http://example.invalid", &config{httpClient: cli})

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
	h := makeHandler(op, backend.URL, &config{httpClient: cli})

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

// ---- truncateBody -----------------------------------------------------

func TestTruncateBody_NoCapPassesThrough(t *testing.T) {
	assert.Equal(t, "hello world", truncateBody([]byte("hello world"), 0))
}

func TestTruncateBody_UnderCapPassesThrough(t *testing.T) {
	assert.Equal(t, "hello", truncateBody([]byte("hello"), 100))
}

func TestTruncateBody_OverCapIsCutAndMarked(t *testing.T) {
	got := truncateBody([]byte("0123456789"), 4)
	assert.True(t, strings.HasPrefix(got, "0123"))
	assert.Contains(t, got, "truncated, 4 of 10 bytes shown")
}

func TestMakeHandler_TruncatesLargeResponse(t *testing.T) {
	// A backend that returns 2000 bytes of 'A' — the handler should cap the
	// body it surfaces to the LLM at 100 bytes and stamp a truncation marker.
	big := strings.Repeat("A", 2000)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, big)
	}))
	t.Cleanup(backend.Close)

	op := loadOp(t, "getPet")
	h := makeHandler(op, backend.URL, &config{httpClient: backend.Client(), maxResponseBytes: 100})

	res := callRaw(t, h, map[string]any{"petId": "1"})
	text := textOf(t, res)
	assert.Contains(t, text, "truncated, 100 of 2000 bytes shown")
	assert.NotContains(t, text, strings.Repeat("A", 200), "truncation must actually drop bytes, not just append a notice")
}

func TestMakeHandler_NoTruncation_WhenMaxIsZero(t *testing.T) {
	big := strings.Repeat("B", 5000)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, big)
	}))
	t.Cleanup(backend.Close)

	op := loadOp(t, "getPet")
	h := makeHandler(op, backend.URL, &config{httpClient: backend.Client(), maxResponseBytes: 0})

	res := callRaw(t, h, map[string]any{"petId": "1"})
	text := textOf(t, res)
	assert.Contains(t, text, big, "max=0 must disable truncation entirely")
	assert.NotContains(t, text, "truncated,")
}

// ---- encodeRequestBody ------------------------------------------------

func TestEncodeRequestBody_JSON_DefaultMarshal(t *testing.T) {
	r, ct, err := encodeRequestBody(map[string]any{"name": "rex"}, "application/json")
	require.NoError(t, err)
	assert.Equal(t, "application/json", ct)
	buf, _ := io.ReadAll(r)
	assert.JSONEq(t, `{"name":"rex"}`, string(buf))
}

func TestEncodeRequestBody_FormURLEncoded(t *testing.T) {
	r, ct, err := encodeRequestBody(map[string]any{
		"name":   "rex",
		"age":    float64(7), // JSON numbers always decode as float64
		"vip":    true,
		"tags":   []any{"a", "b"}, // non-scalar falls through stringify → JSON
		"nilkey": nil,
	}, "application/x-www-form-urlencoded")
	require.NoError(t, err)
	assert.Equal(t, "application/x-www-form-urlencoded", ct)

	buf, _ := io.ReadAll(r)
	q, parseErr := url.ParseQuery(string(buf))
	require.NoError(t, parseErr)
	assert.Equal(t, "rex", q.Get("name"))
	assert.Equal(t, "7", q.Get("age"))
	assert.Equal(t, "true", q.Get("vip"))
	assert.Equal(t, `["a","b"]`, q.Get("tags"))
	assert.Equal(t, "", q.Get("nilkey"))
}

func TestEncodeRequestBody_FormURLEncoded_RejectsNonObject(t *testing.T) {
	_, _, err := encodeRequestBody("not-an-object", "application/x-www-form-urlencoded")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be a JSON object")
}

func TestEncodeRequestBody_Multipart_Fields(t *testing.T) {
	r, ct, err := encodeRequestBody(map[string]any{
		"name": "rex",
		"age":  float64(7),
	}, "multipart/form-data")
	require.NoError(t, err)

	// FormDataContentType appends a boundary; assert prefix only.
	assert.True(t, strings.HasPrefix(ct, "multipart/form-data; boundary="), "got %q", ct)
	_, params, parseErr := mime.ParseMediaType(ct)
	require.NoError(t, parseErr)
	boundary := params["boundary"]
	require.NotEmpty(t, boundary)

	mr := multipart.NewReader(r, boundary)
	seen := map[string]string{}
	for {
		p, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		buf, _ := io.ReadAll(p)
		seen[p.FormName()] = string(buf)
	}
	assert.Equal(t, "rex", seen["name"])
	assert.Equal(t, "7", seen["age"])
}

func TestEncodeRequestBody_Multipart_FileField(t *testing.T) {
	const fileBody = "hello bytes"
	enc := base64.StdEncoding.EncodeToString([]byte(fileBody))
	r, ct, err := encodeRequestBody(map[string]any{
		"label": "first",
		"file": map[string]any{
			"filename":       "note.txt",
			"content_base64": enc,
		},
	}, "multipart/form-data")
	require.NoError(t, err)

	_, params, _ := mime.ParseMediaType(ct)
	mr := multipart.NewReader(r, params["boundary"])

	var fileName, fileContent, label string
	for {
		p, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		switch p.FormName() {
		case "label":
			b, _ := io.ReadAll(p)
			label = string(b)
		case "file":
			fileName = p.FileName()
			b, _ := io.ReadAll(p)
			fileContent = string(b)
		}
	}
	assert.Equal(t, "first", label)
	assert.Equal(t, "note.txt", fileName)
	assert.Equal(t, fileBody, fileContent)
}

func TestEncodeRequestBody_Multipart_InvalidBase64Errors(t *testing.T) {
	_, _, err := encodeRequestBody(map[string]any{
		"file": map[string]any{
			"filename":       "x.bin",
			"content_base64": "!!! not base64 !!!",
		},
	}, "multipart/form-data")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid content_base64")
}

func TestEncodeRequestBody_UnknownMediaType_FallsBackToJSON(t *testing.T) {
	// XML, octet-stream, etc. — the library still JSON-marshals so existing
	// specs that relied on the historic behavior don't break.
	r, ct, err := encodeRequestBody(map[string]any{"k": "v"}, "application/xml")
	require.NoError(t, err)
	assert.Equal(t, "application/xml", ct, "Content-Type must mirror the spec, not be rewritten to application/json")
	buf, _ := io.ReadAll(r)
	assert.JSONEq(t, `{"k":"v"}`, string(buf))
}

package renseijin

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

type regressionTransport func(*http.Request) (*http.Response, error)

func (f regressionTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func regressionOK(body io.ReadCloser) *http.Response {
	return &http.Response{StatusCode: 200, Status: "200 OK", Header: http.Header{}, Body: body}
}

const recursiveInputSpec = `openapi: 3.0.3
info: {title: tree, version: '1'}
paths:
  /tree:
    post:
      operationId: tree
      requestBody:
        required: true
        content:
          application/json:
            schema: {$ref: '#/components/schemas/Node'}
      responses: {'200': {description: ok}}
components:
  schemas:
    Node:
      type: object
      required: [name]
      properties:
        name: {type: string}
        child: {$ref: '#/components/schemas/Node'}
`

func TestRecursiveInputSchemaRegistersAndValidatesDescendants(t *testing.T) {
	d, err := LoadData([]byte(recursiveInputSpec))
	require.NoError(t, err)
	require.NoError(t, d.T.Validate(context.Background()))
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	require.NoError(t, Register(srv, d, WithBaseURL("https://example.test")))
	op := collectOperations(d.T)[0]
	schema := buildInputSchema(op)
	_, err = json.Marshal(schema)
	require.NoError(t, err)
	v, err := compileInputSchema(schema)
	require.NoError(t, err)
	require.NoError(t, v.Validate(map[string]any{"body": map[string]any{"name": "parent", "child": map[string]any{"name": "child"}}}))
	require.Error(t, v.Validate(map[string]any{"body": map[string]any{"name": "parent", "child": map[string]any{"name": 12}}}))
}

func TestSchemaCycleThroughArrayAndEscapedProperty(t *testing.T) {
	node := openapi3.NewObjectSchema()
	child := openapi3.NewObjectSchema()
	child.Properties = map[string]*openapi3.SchemaRef{"self": {Value: child}, "parent": {Value: node}}
	node.Properties = map[string]*openapi3.SchemaRef{"children/~": {Value: openapi3.NewArraySchema().WithItems(child)}}
	schema := schemaToMap(&openapi3.SchemaRef{Value: node})
	v, err := compileInputSchema(schema)
	require.NoError(t, err)
	require.NoError(t, v.Validate(map[string]any{"children/~": []any{map[string]any{"self": map[string]any{}}}}))
	require.Error(t, v.Validate(map[string]any{"children/~": []any{map[string]any{"self": "not a node"}}}))
	require.Error(t, v.Validate(map[string]any{"children/~": []any{"not a node"}}))
}

func TestRequestSchemaPreservesNullableAndObjectConstraints(t *testing.T) {
	const spec = `openapi: 3.0.3
info: {title: constraints, version: '1'}
paths:
  /items:
    post:
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              additionalProperties: false
              required: [serverId, name, count]
              properties:
                serverId: {type: integer, readOnly: true}
                name: {type: string, nullable: true, minLength: 2}
                count: {type: integer, minimum: 0, exclusiveMinimum: true}
      responses: {'200': {description: ok}}
`
	op := collectOperations(loadT(t, spec))[0]
	v, err := compileInputSchema(buildInputSchema(op))
	require.NoError(t, err)
	require.NoError(t, v.Validate(map[string]any{"body": map[string]any{"name": nil, "count": json.Number("1")}}))
	for _, body := range []any{
		map[string]any{"name": "a", "count": 1},
		map[string]any{"name": "ok", "count": 0},
		map[string]any{"name": "ok", "count": 1, "unexpected": true},
	} {
		require.Error(t, v.Validate(map[string]any{"body": body}))
	}
}

func TestHandlerRejectsMalformedArgumentObjects(t *testing.T) {
	client := &http.Client{Transport: regressionTransport(func(*http.Request) (*http.Response, error) {
		t.Error("invalid arguments reached HTTP transport")
		return regressionOK(io.NopCloser(strings.NewReader("ok"))), nil
	})}
	h := makeHandler(loadOp(t, "getPet"), "https://example.test", &config{httpClient: client})
	for _, raw := range []string{`null`, `[]`, `{"petId":"1"} {}`, `{"petId":"1","extra":true}`} {
		res, err := h(context.Background(), &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: json.RawMessage(raw)}})
		require.NoError(t, err)
		require.True(t, res.IsError)
	}
}

func TestRegistrationRejectsInvalidSchemaAndEncoding(t *testing.T) {
	for _, kind := range []string{"schema", "encoding", "response limit"} {
		t.Run(kind, func(t *testing.T) {
			d, err := LoadData([]byte(handlerSpec))
			require.NoError(t, err)
			p := d.T.Paths.Value("/things").Get.Parameters[0].Value
			opts := []Option{WithBaseURL("https://example.test")}
			switch kind {
			case "schema":
				p.Schema.Value.Pattern = "["
			case "encoding":
				p.Style = "matrix"
			case "response limit":
				opts = append(opts, WithMaxResponseBytes(-1))
			}
			srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
			require.Error(t, Register(srv, d, opts...))
		})
	}
}

func TestServerVariablesAllowEmptyDefaults(t *testing.T) {
	got, err := resolveServers(openapi3.Servers{&openapi3.Server{URL: "https://example.test/{prefix}", Variables: map[string]*openapi3.ServerVariable{"prefix": {Default: ""}}}}, nil)
	require.NoError(t, err)
	require.Equal(t, "https://example.test/", got)
}

func TestHandlerPreservesIntegerPrecisionInPathQueryAndBody(t *testing.T) {
	const spec = `openapi: 3.0.3
info: {title: ints, version: '1'}
paths:
  /items/{id}:
    post:
      parameters:
        - {name: id, in: path, required: true, schema: {type: integer, format: int64}}
        - {name: cursor, in: query, schema: {type: integer, format: int64}}
      requestBody:
        content:
          application/json:
            schema: {type: object, properties: {id: {type: integer, format: int64}}}
      responses: {'200': {description: ok}}
`
	op := collectOperations(loadT(t, spec))[0]
	for _, n := range []string{"9007199254740993", "9223372036854775807", "-9223372036854775808"} {
		t.Run(n, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: regressionTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				require.Equal(t, "/items/"+n, r.URL.Path)
				require.Equal(t, n, r.URL.Query().Get("cursor"))
				b, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				require.Equal(t, `{"id":`+n+`}`, string(b))
				return regressionOK(io.NopCloser(strings.NewReader("ok"))), nil
			})}
			h := makeHandler(op, "https://example.test", &config{httpClient: client})
			res, err := h(context.Background(), &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: json.RawMessage(`{"id":` + n + `,"cursor":` + n + `,"body":{"id":` + n + `}}`)}})
			require.NoError(t, err)
			require.False(t, res.IsError, textOf(t, res))
			require.Equal(t, 1, calls)
		})
	}
}

type endlessResponse struct {
	read   int
	closed bool
}

func (r *endlessResponse) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	r.read += len(p)
	return len(p), nil
}
func (r *endlessResponse) Close() error { r.closed = true; return nil }

func TestHandlerBoundsAndClosesEndlessResponse(t *testing.T) {
	body := &endlessResponse{}
	client := &http.Client{Transport: regressionTransport(func(*http.Request) (*http.Response, error) { return regressionOK(body), nil })}
	h := makeHandler(loadOp(t, "getPet"), "https://example.test", &config{httpClient: client, maxResponseBytes: 16})
	res := callRaw(t, h, map[string]any{"petId": "1"})
	require.False(t, res.IsError)
	require.Equal(t, 17, body.read)
	require.True(t, body.closed)
	require.Contains(t, textOf(t, res), "truncated, 16 bytes shown")
}

func TestReadResponseBodyBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		max        int
		want       string
	}{
		{"short", "abc", 4, "abc"}, {"exact", "abcd", 4, "abcd"}, {"overflow", "abcde", 4, "abcd\n... (truncated, 4 bytes shown)"}, {"disabled", "abcdef", 0, "abcdef"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := readResponseBody(strings.NewReader(tc.body), tc.max)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestParameterAliasesPreserveIndependentValues(t *testing.T) {
	op := loadOp(t, "createPet")
	op.pathTmpl = "/pets/{id}"
	op.parameters = openapi3.Parameters{
		{Value: &openapi3.Parameter{Name: "id", In: "path", Required: true, Schema: &openapi3.SchemaRef{Value: openapi3.NewStringSchema()}}},
		{Value: &openapi3.Parameter{Name: "id", In: "query", Schema: &openapi3.SchemaRef{Value: openapi3.NewIntegerSchema()}}},
		{Value: &openapi3.Parameter{Name: "body", In: "query", Schema: &openapi3.SchemaRef{Value: openapi3.NewStringSchema()}}},
		{Value: &openapi3.Parameter{Name: "path.id", In: "header", Schema: &openapi3.SchemaRef{Value: openapi3.NewStringSchema()}}},
	}
	args := map[string]any{"path.id_": "pet-123", "query.id": json.Number("7"), "query.body": "query-value", "path.id": "trace", "body": map[string]any{"name": "rex"}}
	schema := buildInputSchema(op)
	props := schema["properties"].(map[string]any)
	require.Len(t, props, 5)
	v, err := compileInputSchema(schema)
	require.NoError(t, err)
	require.NoError(t, v.Validate(args))
	req, err := buildHTTPRequest(context.Background(), op, "https://example.test", args)
	require.NoError(t, err)
	require.Equal(t, "/pets/pet-123", req.URL.Path)
	require.Equal(t, "7", req.URL.Query().Get("id"))
	require.Equal(t, "query-value", req.URL.Query().Get("body"))
	require.Equal(t, "trace", req.Header.Get("path.id"))
	b, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	require.JSONEq(t, `{"name":"rex"}`, string(b))
}

func TestNonJSONMediaSelectionIsStableAndMatchesSchema(t *testing.T) {
	rb := &openapi3.RequestBody{Content: openapi3.Content{
		"multipart/form-data":               &openapi3.MediaType{Schema: &openapi3.SchemaRef{Value: openapi3.NewObjectSchema()}},
		"application/x-www-form-urlencoded": &openapi3.MediaType{Schema: &openapi3.SchemaRef{Value: openapi3.NewObjectSchema()}},
	}}
	op := operation{method: "POST", pathTmpl: "/upload", op: &openapi3.Operation{RequestBody: &openapi3.RequestBodyRef{Value: rb}}, contentType: pickRequestContentType(rb)}
	for range 100 {
		_, mt := preferredBodySchema(rb)
		require.Equal(t, "application/x-www-form-urlencoded", mt)
		schema := buildInputSchema(op)["properties"].(map[string]any)["body"].(map[string]any)
		req, err := buildHTTPRequest(context.Background(), op, "https://example.test", map[string]any{"body": map[string]any{"name": "value"}})
		require.NoError(t, err)
		require.Equal(t, schema["x-media-type"], req.Header.Get("Content-Type"))
	}
}

func TestMultipartSchemaEnvelopeProducesFileParts(t *testing.T) {
	const spec = `openapi: 3.0.3
info: {title: upload, version: '1'}
paths:
  /upload:
    post:
      requestBody:
        required: true
        content:
          multipart/form-data:
            schema:
              type: object
              required: [file]
              properties:
                file: {type: string, format: binary}
                files: {type: array, items: {type: string, format: binary}}
      responses: {'200': {description: ok}}
`
	op := collectOperations(loadT(t, spec))[0]
	schema := buildInputSchema(op)
	v, err := compileInputSchema(schema)
	require.NoError(t, err)
	envelope := map[string]any{"filename": "note.txt", "content_base64": "aGVsbG8="}
	args := map[string]any{"body": map[string]any{"file": envelope, "files": []any{envelope, envelope}}}
	require.NoError(t, v.Validate(args))
	require.Error(t, v.Validate(map[string]any{"body": map[string]any{"file": "aGVsbG8="}}))
	require.Error(t, v.Validate(map[string]any{"body": map[string]any{"file": map[string]any{"filename": "note.txt"}}}))
	req, err := buildHTTPRequest(context.Background(), op, "https://example.test", args)
	require.NoError(t, err)
	_, params, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	require.NoError(t, err)
	mr := multipart.NewReader(req.Body, params["boundary"])
	for range 3 {
		part, err := mr.NextPart()
		require.NoError(t, err)
		require.Equal(t, "note.txt", part.FileName())
		b, err := io.ReadAll(part)
		require.NoError(t, err)
		require.Equal(t, "hello", string(b))
	}
	_, err = mr.NextPart()
	require.ErrorIs(t, err, io.EOF)
}

package openapi_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/junkd0g/renseijin/openapi"
)

// petstoreDoc loads the example spec once per test that needs it.
func petstoreDoc(t *testing.T) *openapi.Doc {
	t.Helper()
	specPath := filepath.Join("..", "examples", "petstore", "petstore.yaml")
	doc, err := openapi.LoadFile(specPath)
	require.NoError(t, err, "LoadFile")
	return doc
}

// connectClient stands up the registered server on an in-memory transport
// and returns a connected client session.
func connectClient(t *testing.T, srv *mcp.Server) *mcp.ClientSession {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	serverT, clientT := mcp.NewInMemoryTransports()

	serverDone := make(chan error, 1)
	go func() { serverDone <- srv.Run(ctx, serverT) }()
	t.Cleanup(func() {
		cancel()
		<-serverDone
	})

	cli := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	cs, err := cli.Connect(ctx, clientT, nil)
	require.NoError(t, err, "client connect")
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func listToolsByName(t *testing.T, cs *mcp.ClientSession) map[string]*mcp.Tool {
	t.Helper()
	got, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err, "ListTools")
	byName := map[string]*mcp.Tool{}
	for _, tool := range got.Tools {
		byName[tool.Name] = tool
	}
	return byName
}

// TestRegister_Petstore loads the example spec, registers it onto an
// in-memory MCP server, then asks an in-memory MCP client to list the tools
// and asserts the shape callers will actually observe over the wire.
func TestRegister_Petstore(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "petstore-test", Version: "0.0.0"}, nil)
	require.NoError(t, openapi.Register(srv, petstoreDoc(t)))

	byName := listToolsByName(t, connectClient(t, srv))

	for _, want := range []string{"listPets", "createPet", "getPet"} {
		assert.Containsf(t, byName, want, "tool %q must be registered", want)
	}

	// getPet must require its path parameter "petId".
	getPet := byName["getPet"]
	schema, ok := getPet.InputSchema.(map[string]any)
	require.Truef(t, ok, "getPet InputSchema: want map[string]any, got %T", getPet.InputSchema)
	assert.True(t, requiredHas(schema, "petId"), "getPet input schema must require petId, got required=%v", schema["required"])
	assert.True(t, hasProperty(schema, "petId"), "getPet input schema must expose property petId, got %v", schema["properties"])

	// createPet must expose a "body" property because it has a request body.
	createPet := byName["createPet"]
	cps, ok := createPet.InputSchema.(map[string]any)
	require.Truef(t, ok, "createPet InputSchema: want map[string]any, got %T", createPet.InputSchema)
	assert.True(t, hasProperty(cps, "body"), "createPet must expose body, got %v", cps["properties"])
}

func TestRegister_NilServerRejected(t *testing.T) {
	err := openapi.Register(nil, petstoreDoc(t))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil *mcp.Server")
}

func TestRegister_NilDocRejected(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "x", Version: "0"}, nil)

	err := openapi.Register(srv, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil document")

	err = openapi.Register(srv, &openapi.Doc{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil document")
}

func TestRegister_WithToolNamePrefix(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "x", Version: "0"}, nil)
	require.NoError(t, openapi.Register(srv, petstoreDoc(t), openapi.WithToolNamePrefix("pets_")))

	byName := listToolsByName(t, connectClient(t, srv))
	assert.Contains(t, byName, "pets_listPets")
	assert.Contains(t, byName, "pets_getPet")
	assert.NotContains(t, byName, "listPets", "unprefixed name must not appear")
}

func TestRegister_ToolDescriptionPrefersSummary(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "x", Version: "0"}, nil)
	require.NoError(t, openapi.Register(srv, petstoreDoc(t)))

	byName := listToolsByName(t, connectClient(t, srv))

	// petstore.yaml has summary "Info for a specific pet" on getPet and no
	// distinct description, so the description should lead with the summary
	// and include the method/path tag.
	desc := byName["getPet"].Description
	assert.Contains(t, desc, "Info for a specific pet")
	assert.Contains(t, desc, "GET /pets/{petId}")
}

const summaryAndDescriptionSpec = `openapi: 3.0.3
info:
  title: d
  version: "1.0"
paths:
  /thing:
    get:
      operationId: getThing
      summary: short summary
      description: long-form description
      responses: {"200": {description: ok}}
  /bare:
    get:
      operationId: getBare
      responses: {"200": {description: ok}}
`

func TestRegister_ToolDescription_JoinsSummaryAndDescription(t *testing.T) {
	doc, err := openapi.LoadData([]byte(summaryAndDescriptionSpec))
	require.NoError(t, err)

	srv := mcp.NewServer(&mcp.Implementation{Name: "x", Version: "0"}, nil)
	require.NoError(t, openapi.Register(srv, doc))

	byName := listToolsByName(t, connectClient(t, srv))

	thing := byName["getThing"].Description
	assert.Contains(t, thing, "short summary")
	assert.Contains(t, thing, "long-form description")
	assert.Contains(t, thing, "(GET /thing)")

	// With neither summary nor description, the tool description falls back
	// to just "METHOD path".
	bare := byName["getBare"].Description
	assert.Equal(t, "GET /bare", bare)
}

func TestRegister_EndToEnd_CallTool_ForwardsToBackend(t *testing.T) {
	var seen *http.Request
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":7,"name":"rex"}`)
	}))
	t.Cleanup(backend.Close)

	srv := mcp.NewServer(&mcp.Implementation{Name: "x", Version: "0"}, nil)
	require.NoError(t, openapi.Register(srv, petstoreDoc(t),
		openapi.WithHTTPClient(backend.Client()),
		openapi.WithBaseURL(backend.URL),
	))

	cs := connectClient(t, srv)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "getPet",
		Arguments: map[string]any{"petId": "7"},
	})
	require.NoError(t, err)

	assert.False(t, res.IsError)
	require.NotNil(t, seen, "backend was not hit")
	assert.Equal(t, "/pets/7", seen.URL.Path)
	assert.Equal(t, http.MethodGet, seen.Method)
}

func TestRegister_WithBaseURL_OverridesSpecServerURL(t *testing.T) {
	var seenHost string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenHost = r.Host
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(backend.Close)

	srv := mcp.NewServer(&mcp.Implementation{Name: "x", Version: "0"}, nil)
	require.NoError(t, openapi.Register(srv, petstoreDoc(t),
		openapi.WithHTTPClient(backend.Client()),
		openapi.WithBaseURL(backend.URL),
	))

	cs := connectClient(t, srv)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "listPets",
		Arguments: map[string]any{},
	})
	require.NoError(t, err)
	assert.False(t, res.IsError)

	// Without WithBaseURL the requests would land on petstore.example.com.
	assert.NotEqual(t, "petstore.example.com", seenHost)
	assert.NotEmpty(t, seenHost)
}

func TestRegister_NoServerURL_FallsBackToEmptyBase(t *testing.T) {
	// Specs without servers[] used to crash; verify Register still succeeds.
	const noServers = `openapi: 3.0.3
info:
  title: x
  version: "1.0"
paths:
  /ping:
    get:
      operationId: ping
      responses: {"200": {description: ok}}
`
	doc, err := openapi.LoadData([]byte(noServers))
	require.NoError(t, err)
	srv := mcp.NewServer(&mcp.Implementation{Name: "x", Version: "0"}, nil)
	require.NoError(t, openapi.Register(srv, doc))
}

// requiredHas reports whether the JSON-Schema-style "required" list of schema
// contains name. The wire form is []any (JSON unmarshal of a string array),
// so we coerce element by element.
func requiredHas(schema map[string]any, name string) bool {
	switch req := schema["required"].(type) {
	case []any:
		for _, v := range req {
			if s, ok := v.(string); ok && s == name {
				return true
			}
		}
	case []string:
		return slices.Contains(req, name)
	}
	return false
}

func hasProperty(schema map[string]any, name string) bool {
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		return false
	}
	_, ok = props[name]
	return ok
}

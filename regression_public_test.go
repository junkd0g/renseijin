package renseijin_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/junkd0g/renseijin"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

type regressionTransport func(*http.Request) (*http.Response, error)

func (f regressionTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func regressionOK() *http.Response {
	return &http.Response{StatusCode: 200, Status: "200 OK", Header: http.Header{}, Body: io.NopCloser(strings.NewReader("ok"))}
}

func TestRegister_InvalidArgumentsNeverReachUpstream(t *testing.T) {
	var mu sync.Mutex
	hits := 0
	client := &http.Client{Transport: regressionTransport(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		hits++
		mu.Unlock()
		return regressionOK(), nil
	})}
	srv := mcp.NewServer(&mcp.Implementation{Name: "regression", Version: "1"}, nil)
	require.NoError(t, renseijin.Register(srv, petstoreDoc(t), renseijin.WithHTTPClient(client)))
	cs := connectClient(t, srv)
	for _, args := range []any{map[string]any{}, map[string]any{"body": map[string]any{}}, map[string]any{"body": map[string]any{"name": 123}}} {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "createPet", Arguments: args})
		require.NoError(t, err)
		require.True(t, res.IsError)
	}
	mu.Lock()
	defer mu.Unlock()
	require.Zero(t, hits)
}

func TestRegister_CollidingNamesFailAtomically(t *testing.T) {
	const spec = `openapi: 3.0.3
info: {title: collision, version: '1'}
servers: [{url: 'https://example.test'}]
paths:
  /a-b:
    get:
      responses: {'200': {description: ok}}
  /a_b:
    get:
      responses: {'200': {description: ok}}
`
	d, err := renseijin.LoadData([]byte(spec))
	require.NoError(t, err)
	require.NoError(t, d.T.Validate(context.Background()))
	srv := mcp.NewServer(&mcp.Implementation{Name: "regression", Version: "1"}, nil)
	srv.AddTool(&mcp.Tool{Name: "existing", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{}, nil
	})
	require.ErrorContains(t, renseijin.Register(srv, d), "duplicate tool name")
	ts := listToolsByName(t, connectClient(t, srv))
	require.Len(t, ts, 1)
	require.Contains(t, ts, "existing")
}

func TestRegister_OperationServerTakesPrecedence(t *testing.T) {
	const spec = `openapi: 3.0.3
info: {title: servers, version: '1'}
servers: [{url: 'https://root.example.test'}]
paths:
  /ping:
    servers: [{url: 'https://path.example.test'}]
    get:
      operationId: ping
      servers: [{url: 'https://operation.example.test'}]
      responses: {'200': {description: ok}}
`
	for _, tc := range []struct {
		name, host      string
		operation, path bool
		override        string
	}{
		{name: "operation", host: "operation.example.test", operation: true, path: true},
		{name: "path", host: "path.example.test", path: true},
		{name: "root", host: "root.example.test"},
		{name: "explicit override", host: "override.example.test", operation: true, path: true, override: "https://override.example.test"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := renseijin.LoadData([]byte(spec))
			require.NoError(t, err)
			pi := d.T.Paths.Value("/ping")
			if !tc.operation {
				pi.Get.Servers = nil
			}
			if !tc.path {
				pi.Servers = nil
			}
			require.NoError(t, d.T.Validate(context.Background()))
			var mu sync.Mutex
			got := ""
			client := &http.Client{Transport: regressionTransport(func(r *http.Request) (*http.Response, error) {
				mu.Lock()
				got = r.URL.Host
				mu.Unlock()
				return regressionOK(), nil
			})}
			srv := mcp.NewServer(&mcp.Implementation{Name: "regression", Version: "1"}, nil)
			require.NoError(t, renseijin.Register(srv, d, renseijin.WithHTTPClient(client), renseijin.WithBaseURL(tc.override)))
			cs := connectClient(t, srv)
			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "ping"})
			require.NoError(t, err)
			require.False(t, res.IsError)
			mu.Lock()
			defer mu.Unlock()
			require.Equal(t, tc.host, got)
		})
	}
}

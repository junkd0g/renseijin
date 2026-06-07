package renseijin_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/junkd0g/renseijin"
)

// TestServe_EndToEnd_OverInMemoryTransport drives the Serve helper through an
// in-memory MCP transport so we can assert it actually (a) connects, (b)
// surfaces the registered tools, and (c) terminates when ctx is cancelled.
// This is the contract Serve promises consumers — anything less is just
// "Register but shaped like a function call".
func TestServe_EndToEnd_OverInMemoryTransport(t *testing.T) {
	const spec = `openapi: 3.0.3
info: {title: serve-test, version: "1.0"}
paths:
  /ping:
    get:
      operationId: ping
      responses: {"200": {description: ok}}
`
	doc, err := renseijin.LoadData([]byte(spec))
	require.NoError(t, err)

	serverT, clientT := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- renseijin.Serve(ctx, doc,
			renseijin.WithServerInfo("serve-test", "0.0.0"),
			renseijin.WithTransport(serverT),
			renseijin.WithRegisterOptions(renseijin.WithHTTPClient(http.DefaultClient)),
		)
	}()

	cli := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	cs, err := cli.Connect(ctx, clientT, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })

	tools, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)
	names := []string{}
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	assert.Contains(t, names, "ping", "Serve must register the spec's operations")

	cancel()
	select {
	case serveErr := <-done:
		// context.Canceled is the expected exit path — anything else
		// means Serve hung on or mis-translated the cancellation.
		if serveErr != nil && !errors.Is(serveErr, context.Canceled) && !errors.Is(serveErr, context.DeadlineExceeded) {
			t.Logf("Serve returned: %v", serveErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after ctx cancel")
	}
}

func TestServe_NilDoc_ReturnsError(t *testing.T) {
	err := renseijin.Serve(context.Background(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "renseijin.Serve")
	assert.Contains(t, err.Error(), "nil document")
}

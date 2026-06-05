package openapi_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/junkd0g/renseijin/openapi"
)

// TestRegister_Petstore loads the example spec, registers it onto an
// in-memory MCP server, then asks an in-memory MCP client to list the tools
// and asserts the shape callers will actually observe over the wire.
func TestRegister_Petstore(t *testing.T) {
	specPath := filepath.Join("..", "examples", "petstore", "petstore.yaml")
	doc, err := openapi.LoadFile(specPath)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}

	srv := mcp.NewServer(&mcp.Implementation{Name: "petstore-test", Version: "0.0.0"}, nil)
	if err := openapi.Register(srv, doc); err != nil {
		t.Fatalf("Register: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	serverT, clientT := mcp.NewInMemoryTransports()

	serverDone := make(chan error, 1)
	go func() { serverDone <- srv.Run(ctx, serverT) }()
	t.Cleanup(func() {
		cancel()
		<-serverDone
	})

	cli := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	cs, err := cli.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	got, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	byName := map[string]*mcp.Tool{}
	for _, tool := range got.Tools {
		byName[tool.Name] = tool
	}

	for _, want := range []string{"listPets", "createPet", "getPet"} {
		if _, ok := byName[want]; !ok {
			names := make([]string, 0, len(byName))
			for n := range byName {
				names = append(names, n)
			}
			t.Fatalf("expected tool %q to be registered; got %v", want, names)
		}
	}

	// getPet must require its path parameter "petId".
	getPet := byName["getPet"]
	schema, ok := getPet.InputSchema.(map[string]any)
	if !ok {
		t.Fatalf("getPet InputSchema: want map[string]any, got %T", getPet.InputSchema)
	}
	if !requiredHas(schema, "petId") {
		t.Errorf("getPet input schema does not require petId: %#v", schema["required"])
	}
	if !hasProperty(schema, "petId") {
		t.Errorf("getPet input schema is missing property petId: %#v", schema["properties"])
	}

	// createPet must expose a "body" property because it has a request body.
	createPet := byName["createPet"]
	cps, ok := createPet.InputSchema.(map[string]any)
	if !ok {
		t.Fatalf("createPet InputSchema: want map[string]any, got %T", createPet.InputSchema)
	}
	if !hasProperty(cps, "body") {
		t.Errorf("createPet input schema is missing property body: %#v", cps["properties"])
	}
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
		for _, s := range req {
			if s == name {
				return true
			}
		}
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

# renseijin

> **Status: under construction.** This is a work-in-progress experiment. The
> public API will change, edges are rough, and many parts of the OpenAPI 3.x
> surface are not yet handled. Do not depend on this in production.

A small Go library that turns an [OpenAPI 3.x](https://spec.openapis.org/oas/v3.1.0)
document into a set of [Model Context Protocol](https://modelcontextprotocol.io)
tools — one MCP tool per OpenAPI operation. You bring an `*mcp.Server` and an
`*http.Client`; `renseijin` wires the tools onto your server and forwards
invocations as HTTP requests against the upstream API.

The library never holds credentials. Anything an LLM client invokes is sent
through the `*http.Client` you pass in, so authentication lives entirely in
that client's transport (bearer tokens, signed requests, mTLS, etc.).

---

## Why

Most APIs are already documented as OpenAPI specs. If you want to expose one
of them to an MCP-aware agent, you typically have to write a tool wrapper per
endpoint by hand. `renseijin` takes the spec as the source of truth and
generates those tools at startup.

Design intent:

- **One MCP tool per operation.** `operationId` becomes the tool name.
- **Caller-owned server.** `Register(srv, doc, opts...)` only calls
  `srv.AddTool` — it does not start, stop, or otherwise mutate the server.
- **Caller-owned HTTP client.** Pass an `*http.Client` whose `Transport`
  carries the credentials. The library has no concept of "auth config".
- **Dynamic input schemas.** Operation inputs aren't known at compile time,
  so tool input schemas are built as `map[string]any` JSON Schema and handed
  to the low-level `Server.AddTool`.

---

## Status of the build

| Item                              | State        |
| --------------------------------- | ------------ |
| Public API stability              | unstable     |
| OpenAPI 3.0.x parsing             | works        |
| OpenAPI 3.1 parsing               | works (basic shapes; not exhaustively tested) |
| Path / query / header / cookie params | works    |
| `application/json` request bodies | works        |
| Non-JSON request bodies           | partial (sent as JSON-marshaled bytes; only Content-Type is set per spec) |
| Response handling                 | returned as text content with status line + raw body |
| Auth                              | delegated to caller's `*http.Client` |
| OAuth helpers                     | not provided (by design) |
| Streaming responses               | not yet      |
| `multipart/form-data`             | not yet      |
| `x-www-form-urlencoded`           | not yet      |
| Server variables (templated `servers[].url`) | not yet substituted |
| Security schemes / `securityRequirements` | inspected for documentation only; not enforced |
| Response schema validation        | not yet      |
| Pagination helpers                | not yet      |

If something on this list lands in front of your use case, expect to read
source and patch.

---

## Requirements

- Go **1.25+** (required by `github.com/modelcontextprotocol/go-sdk` v1.6.0)
- `github.com/modelcontextprotocol/go-sdk` **v1.6.0**
- `github.com/getkin/kin-openapi` **v0.140.0**

These exact versions are pinned in `go.mod`. The SDK's low-level `AddTool`
API and `Tool.InputSchema` semantics were verified against the v1.6.0
sources; the kin-openapi model shape (`Paths.Map`, `Operation`, `Parameter`,
`SchemaRef`, `Types`) was verified against v0.140.0.

---

## Install

```sh
go get github.com/junkd0g/renseijin@latest
```

---

## Quick start

Register every operation in a spec onto an MCP server speaking over stdio:

```go
package main

import (
    "context"
    "log"
    "net/http"

    "github.com/modelcontextprotocol/go-sdk/mcp"

    "github.com/junkd0g/renseijin"
)

func main() {
    doc, err := renseijin.LoadFile("petstore.yaml")
    if err != nil {
        log.Fatal(err)
    }

    srv := mcp.NewServer(&mcp.Implementation{
        Name:    "petstore",
        Version: "0.1.0",
    }, nil)

    if err := renseijin.Register(srv, doc,
        renseijin.WithHTTPClient(http.DefaultClient),
    ); err != nil {
        log.Fatal(err)
    }

    if err := srv.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
        log.Fatal(err)
    }
}
```

A complete example lives in [`examples/petstore`](./examples/petstore/).

### Adding authentication

Auth is a property of the HTTP client, not of `renseijin`:

```go
client := &http.Client{
    Transport: &bearerTransport{
        token: os.Getenv("API_TOKEN"),
        base:  http.DefaultTransport,
    },
}

renseijin.Register(srv, doc, renseijin.WithHTTPClient(client))
```

```go
type bearerTransport struct {
    token string
    base  http.RoundTripper
}

func (t *bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
    r = r.Clone(r.Context())
    r.Header.Set("Authorization", "Bearer "+t.token)
    return t.base.RoundTrip(r)
}
```

### Overriding the upstream URL

When the spec's `servers[].url` is wrong for your environment (sandbox,
staging, local mock), override it:

```go
renseijin.Register(srv, doc,
    renseijin.WithHTTPClient(client),
    renseijin.WithBaseURL("https://sandbox.example.com/v1"),
)
```

### Tool name collisions

When stacking several specs on one server, prefix tool names:

```go
renseijin.Register(srv, petstoreDoc, renseijin.WithToolNamePrefix("pets_"))
renseijin.Register(srv, billingDoc,  renseijin.WithToolNamePrefix("billing_"))
```

---

## Tool input shape

For each operation, the generated tool's input schema is an object with:

- one property per non-body parameter, named after the parameter
  (`petId`, `limit`, ...). Each is tagged with `x-in: path|query|header|cookie`
  so callers introspecting the schema can see where the value goes on the
  wire.
- a `body` property when the operation declares a request body. The body
  schema mirrors the `application/json` media type when available.

Required parameters land in the top-level `required` array. Required request
bodies add `"body"` to that array.

Example, for `GET /pets/{petId}`:

```json
{
  "type": "object",
  "properties": {
    "petId": { "type": "string", "x-in": "path", "description": "..." }
  },
  "required": ["petId"]
}
```

---

## Repository layout

```
go.mod
doc.go         -- LoadFile / LoadData / FromT
options.go     -- functional options
operation.go   -- spec walk, parameter merging, name derivation
schema.go      -- *openapi3.SchemaRef → map[string]any JSON Schema
handler.go     -- mcp.ToolHandler → outbound *http.Request
register.go    -- public Register entry point
*_test.go      -- per-source white/black-box tests (testify)
examples/
  petstore/
    main.go
    petstore.yaml
```

---

## Development

```sh
go mod tidy
go build ./...
go vet ./...
gofmt -l .
go test ./...
```

The test suite loads `examples/petstore/petstore.yaml`, registers it onto
an in-memory MCP server, then asks an in-memory client to list and call
tools — so it exercises the same path callers will use over the
wire.

---

## Naming

"Renseijin" (錬成陣) is the Japanese term for a transmutation circle —
something that transforms one structured artifact into another. This library
transmutes an OpenAPI document into MCP tools.

---

## License

Not chosen yet. Until that changes, treat the code as "all rights reserved" —
fine for evaluation, not for redistribution.

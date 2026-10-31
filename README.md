# renseijin

[![Go Report Card](https://goreportcard.com/badge/github.com/junkd0g/renseijin)](https://goreportcard.com/report/github.com/junkd0g/renseijin)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](https://opensource.org/licenses/MIT)
[![GoDoc](https://pkg.go.dev/badge/github.com/junkd0g/renseijin.svg)](https://pkg.go.dev/github.com/junkd0g/renseijin)

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
| Path / query / header / cookie params | scalars and flat arrays/objects with supported OpenAPI styles; unsupported encodings fail explicitly |
| Input validation                  | generated JSON Schema is compiled at registration and enforced before HTTP dispatch; recursive schemas use local references |
| `application/json` request bodies | works        |
| `application/x-www-form-urlencoded` | works with default form serialization (body must be a JSON object; arrays become repeated fields) |
| `multipart/form-data`             | works for text fields + base64-encoded file uploads (`{filename, content_base64}`) |
| Non-JSON / non-form request bodies | partial (sent as JSON-marshaled bytes; only Content-Type is set per spec) |
| Response handling                 | text content with status line + body (truncated past `WithMaxResponseBytes`, default 64 KiB) |
| Auth                              | delegated to caller's `*http.Client` |
| OAuth helpers                     | not provided (by design) |
| Streaming responses               | not yet      |
| Server variables (templated `servers[].url`) | works at operation, path, and root levels (spec defaults + `WithServerVariables` overrides; unresolved placeholders fail at `Register`) |
| Security schemes / `securityRequirements` | inspected for documentation only; not enforced |
| Response schema validation        | not yet      |
| Pagination helpers                | not yet      |
| Convenience entry point           | `renseijin.Serve(ctx, doc, opts...)` |

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

### One-liner: `renseijin.Serve`

If you don't need to share the server with anything else, skip the
`NewServer` + `Register` + `Run` dance:

```go
err := renseijin.Serve(ctx, doc,
    renseijin.WithServerInfo("petstore", "0.1.0"),
    renseijin.WithRegisterOptions(
        renseijin.WithHTTPClient(client),
        renseijin.WithBaseURL("https://sandbox.example.com/v1"),
    ),
)
```

For multi-spec composition or custom server hooks, keep using `Register` —
`Serve` is intentionally opinionated (defaults to stdio, single server).

### Server-variable substitution

For specs like

```yaml
servers:
  - url: https://{region}.api.example.com/{version}
    variables:
      region:  {default: us-east-1}
      version: {default: v1}
```

`Register` selects operation-level servers before path-level servers, then
root-level servers, using the first non-empty URL in the selected list.
`WithBaseURL` overrides all three levels. Server-variable defaults are
substituted automatically. To target a different
region without editing the spec:

```go
renseijin.Register(srv, doc,
    renseijin.WithServerVariables(map[string]string{"region": "eu-west-1"}),
)
```

Any placeholder that has neither an override nor a spec-side default causes
`Register` to return an error at startup — better than a tool that fails
every call with a DNS error.

### Form-encoded request bodies

`application/x-www-form-urlencoded` operations expect a JSON-object body;
scalar values become form fields, arrays become repeated fields, and flat
objects expand into their own fields, following the default `form` style
with `explode: true`.

`multipart/form-data` operations take the same shape, with one extension:
any value of the form `{"filename": "...", "content_base64": "..."}`
becomes a file part. The base64 envelope is needed because tool arguments
travel as JSON, which has no binary type. The generated input schema translates
`type: string, format: binary` properties into this envelope, including array
items. Arrays of files produce repeated file parts. Both envelope fields are
required; the filename must be non-empty.

When several request media types are available, registration selects one once:
`application/json`, then `application/x-www-form-urlencoded`, then
`multipart/form-data`, then the first remaining type in lexical order. The
advertised schema and HTTP encoder use that same selection.

### Response truncation

By default the handler folds the first 64 KiB of each response body into
the tool result, with a `(truncated, N bytes shown)` marker on overflow.
The handler reads at most the cap plus one byte, then closes the response;
it does not drain the body to determine its total length. Tune the cap
(or disable it with `0`) via `WithMaxResponseBytes`. Negative limits fail at
registration. Request timeouts remain the responsibility of the supplied HTTP
client and the call context.

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

Within one registration, duplicate generated names cause an error before any
tools are added. Assign unique `operationId` values when fallback names collide.
When stacking several specs on one server, use distinct prefixes: the SDK does
not expose an inventory of tools for checking names already on the server.

```go
renseijin.Register(srv, petstoreDoc, renseijin.WithToolNamePrefix("pets_"))
renseijin.Register(srv, billingDoc,  renseijin.WithToolNamePrefix("billing_"))
```

---

## Tool input shape

For each operation, the generated tool's input schema is an object with:

- one property per non-body parameter, named after the parameter
  (`petId`, `limit`, ...). Each is tagged with `x-in: path|query|header|cookie`
  and `x-name` (the original parameter name)
  so callers introspecting the schema can see where the value goes on the
  wire.
- a `body` property when the operation declares a request body. The body
  schema mirrors the `application/json` media type when available.

Required parameters land in the top-level `required` array. Required request
bodies add `"body"` to that array.

If parameters share a name across locations, or a parameter named `body`
conflicts with a request body, they receive aliases such as `path.id`,
`query.id`, or `query.body`. If an alias itself conflicts with an original
parameter name, underscores are appended until it is unique. Read the generated
schema to obtain the exact names. Ordinary, unambiguous names stay unchanged.

Tool arguments are validated against the advertised schema before any HTTP
request is sent. Unknown top-level arguments, missing required fields, and
invalid types or constraints return tool errors. JSON numbers retain their
original precision, including 64-bit integer IDs. Do not mutate the document
after registration.

Parameter serialization supports `simple` for paths and headers, `label` and
`matrix` for paths, and `form` for query strings and cookies. Queries also
support `spaceDelimited` and `pipeDelimited` arrays with `explode: false`, and
`deepObject` objects with `explode: true`. Nested arrays/objects in parameters
return errors. Unsupported styles, content-based parameters, and
`allowReserved: true` fail at registration.

Example, for `GET /pets/{petId}`:

```json
{
  "type": "object",
  "properties": {
    "petId": { "type": "string", "x-in": "path", "x-name": "petId", "description": "..." }
  },
  "required": ["petId"],
  "additionalProperties": false
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
handler.go     -- mcp.ToolHandler → outbound *http.Request, body encoding,
                   response truncation
parameters.go  -- OpenAPI parameter styles and serialization
register.go    -- public Register entry point, server-variable resolution
serve.go       -- Serve convenience helper (NewServer + Register + Run)
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

## Contributing

1. Fork the repository.
2. Create a feature branch (`git checkout -b feature/your-feature`).
3. Commit your changes.
4. Push to the branch (`git push origin feature/your-feature`).
5. Open a pull request.

## License

This project is licensed under the MIT License — see the [LICENSE](LICENSE)
file for details.

## Author

Iordanis Paschalidis — [@junkd0g](https://github.com/junkd0g)

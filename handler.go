package renseijin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// makeHandler returns the mcp.ToolHandler that turns a tool invocation into
// an outbound HTTP request and the response into a tool result.
//
// The handler is closed over the spec-derived operation and the Register-time
// config (so all per-call concerns — http.Client, response-size cap — live
// in one place rather than as a growing parameter list).
func makeHandler(op operation, baseURL string, cfg *config) mcp.ToolHandler {
	client := cfg.httpClient
	maxBytes := cfg.maxResponseBytes
	validator := op.inputValidator
	var schemaErr error
	if validator == nil {
		validator, schemaErr = compileInputSchema(buildInputSchema(op))
	}
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if schemaErr != nil {
			return errResult(fmt.Errorf("invalid input schema: %w", schemaErr)), nil
		}
		args := map[string]any{}
		if raw := req.Params.Arguments; len(raw) > 0 {
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.UseNumber()
			if err := decoder.Decode(&args); err != nil {
				return errResult(fmt.Errorf("invalid JSON arguments: %w", err)), nil
			}
			if err := decoder.Decode(new(any)); err != io.EOF {
				return errResult(fmt.Errorf("invalid JSON arguments: expected a single object")), nil
			}
		}
		if args == nil {
			return errResult(fmt.Errorf("arguments must be an object")), nil
		}
		if err := validator.Validate(args); err != nil {
			return errResult(fmt.Errorf("invalid arguments: %w", err)), nil
		}

		httpReq, err := buildHTTPRequest(ctx, op, baseURL, args)
		if err != nil {
			return errResult(err), nil
		}

		resp, err := client.Do(httpReq)
		if err != nil {
			return errResult(fmt.Errorf("http: %w", err)), nil
		}
		defer func() { _ = resp.Body.Close() }()

		body, err := readResponseBody(resp.Body, maxBytes)
		if err != nil {
			return errResult(fmt.Errorf("read body: %w", err)), nil
		}

		text := fmt.Sprintf("HTTP %d %s\n%s", resp.StatusCode, http.StatusText(resp.StatusCode), body)
		result := &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: text}},
		}
		if resp.StatusCode >= 400 {
			result.IsError = true
		}
		return result, nil
	}
}

// Read no more than max bytes plus one overflow probe. The total length of a
// truncated stream is intentionally unknown; draining it would defeat the cap.
func readResponseBody(r io.Reader, max int) (string, error) {
	if max == 0 {
		body, err := io.ReadAll(r)
		return string(body), err
	}
	if max < 0 {
		return "", fmt.Errorf("max response bytes must be non-negative")
	}
	body, err := io.ReadAll(io.LimitReader(r, int64(max)))
	if err != nil {
		return "", err
	}
	if len(body) < max {
		return string(body), nil
	}
	var probe [1]byte
	n, err := io.ReadFull(r, probe[:])
	if err != nil && err != io.EOF {
		return "", err
	}
	if n > 0 {
		return string(body) + fmt.Sprintf("\n... (truncated, %d bytes shown)", max), nil
	}
	return string(body), nil
}

func errResult(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
	}
}

// buildHTTPRequest assembles the outbound *http.Request from the operation,
// base URL, and the decoded tool arguments.
func buildHTTPRequest(ctx context.Context, op operation, baseURL string, args map[string]any) (*http.Request, error) {
	pathTmpl := op.pathTmpl
	query := url.Values{}
	header := http.Header{}
	cookies := []*http.Cookie{}
	names := op.parameterNames()

	for _, pref := range op.parameters {
		if pref == nil || pref.Value == nil {
			continue
		}
		p := pref.Value
		raw, present := args[names[p]]
		if !present {
			if p.Required {
				return nil, fmt.Errorf("missing required parameter %q", p.Name)
			}
			continue
		}
		if err := serializeParameter(p, raw, &pathTmpl, query, header, &cookies); err != nil {
			return nil, err
		}
	}

	endpoint, err := url.Parse(strings.TrimRight(baseURL, "/") + pathTmpl)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	if len(query) > 0 {
		endpoint.RawQuery = query.Encode()
	}

	var body io.Reader
	contentType := ""
	if op.op.RequestBody != nil && op.op.RequestBody.Value != nil {
		rb, present := args["body"]
		if !present && op.op.RequestBody.Value.Required {
			return nil, fmt.Errorf("missing required body")
		}
		if present {
			mediaType := op.requestContentType()
			body, contentType, err = encodeRequestBody(rb, mediaType)
			if err != nil {
				return nil, err
			}
		}
	}

	httpReq, err := http.NewRequestWithContext(ctx, op.method, endpoint.String(), body)
	if err != nil {
		return nil, err
	}
	for k, vs := range header {
		for _, v := range vs {
			httpReq.Header.Add(k, v)
		}
	}
	for _, c := range cookies {
		httpReq.AddCookie(c)
	}
	if contentType != "" {
		httpReq.Header.Set("Content-Type", contentType)
	}
	httpReq.Header.Set("Accept", "application/json")
	return httpReq, nil
}

// encodeRequestBody serializes the "body" argument according to the chosen
// media type. The returned contentType may differ from the input mediaType:
// multipart/form-data appends the writer's boundary so the receiver can split
// the body back into parts.
//
// Three media types are first-class:
//   - application/json (and the * fallback): JSON-marshal whatever the model
//     sent. Numbers stay numbers, nesting is preserved.
//   - application/x-www-form-urlencoded: the body must be a JSON object;
//     fields use the default form style with explode=true.
//   - multipart/form-data: the body must be a JSON object. A value shaped
//     like {filename, content_base64} becomes a file part; anything else
//     becomes a plain field. We use base64 because tool arguments travel as
//     JSON, which has no native binary encoding.
//
// Any other Content-Type (XML, octet-stream, ...) falls through to the JSON
// path, matching the pre-existing "send as JSON-marshaled bytes" behavior so
// we don't break existing specs.
func encodeRequestBody(rb any, mediaType string) (io.Reader, string, error) {
	switch mediaType {
	case "application/x-www-form-urlencoded":
		obj, ok := rb.(map[string]any)
		if !ok {
			return nil, "", fmt.Errorf("body for %s must be a JSON object, got %T", mediaType, rb)
		}
		vals := url.Values{}
		for k, v := range obj {
			path := ""
			cookies := []*http.Cookie{}
			if err := serializeParameter(&openapi3.Parameter{Name: k, In: "query"}, v, &path, vals, nil, &cookies); err != nil {
				return nil, "", err
			}
		}
		return strings.NewReader(vals.Encode()), mediaType, nil

	case "multipart/form-data":
		obj, ok := rb.(map[string]any)
		if !ok {
			return nil, "", fmt.Errorf("body for %s must be a JSON object, got %T", mediaType, rb)
		}
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		// Keep field order stable; the MIME boundary remains random.
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if err := writeMultipartField(mw, k, obj[k]); err != nil {
				return nil, "", err
			}
		}
		if err := mw.Close(); err != nil {
			return nil, "", fmt.Errorf("close multipart writer: %w", err)
		}
		return &buf, mw.FormDataContentType(), nil

	default:
		buf, err := json.Marshal(rb)
		if err != nil {
			return nil, "", fmt.Errorf("marshal body: %w", err)
		}
		return bytes.NewReader(buf), mediaType, nil
	}
}

// writeMultipartField emits one (key, value) pair as either a file part
// (when the value is a {filename, content_base64} object) or a plain text
// field. See encodeRequestBody for the encoding contract.
func writeMultipartField(mw *multipart.Writer, name string, v any) error {
	if items, ok := v.([]any); ok {
		for _, item := range items {
			if err := writeMultipartField(mw, name, item); err != nil {
				return err
			}
		}
		return nil
	}
	if obj, ok := v.(map[string]any); ok {
		if fn, ok := obj["filename"].(string); ok && fn != "" {
			enc, ok := obj["content_base64"].(string)
			if !ok {
				return fmt.Errorf("multipart field %q: content_base64 must be a string", name)
			}
			raw, err := base64.StdEncoding.DecodeString(enc)
			if err != nil {
				return fmt.Errorf("multipart field %q: invalid content_base64: %w", name, err)
			}
			fw, err := mw.CreateFormFile(name, fn)
			if err != nil {
				return fmt.Errorf("multipart field %q: %w", name, err)
			}
			if _, err := fw.Write(raw); err != nil {
				return fmt.Errorf("multipart field %q: %w", name, err)
			}
			return nil
		}
	}
	return mw.WriteField(name, stringify(v))
}

// stringify renders a JSON-decoded value (string/number/bool/etc.) into the
// string form expected on the wire for path/query/header values.
func stringify(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return string(x)
	case float64:
		// Support Go callers of the internal encoder as well as json.Number
		// values from tool arguments. Integral floats have no trailing ".0".
		if x == float64(int64(x)) {
			return fmt.Sprintf("%d", int64(x))
		}
		return fmt.Sprintf("%g", x)
	case bool:
		if x {
			return "true"
		}
		return "false"
	case nil:
		return ""
	default:
		buf, _ := json.Marshal(x)
		return string(buf)
	}
}

func pickRequestContentType(rb *openapi3.RequestBody) string {
	if rb == nil || rb.Content == nil {
		return "application/json"
	}
	for _, name := range []string{"application/json", "application/x-www-form-urlencoded", "multipart/form-data"} {
		if mt := rb.Content[name]; mt != nil {
			return name
		}
	}
	var names []string
	for name, mt := range rb.Content {
		if mt != nil {
			names = append(names, name)
		}
	}
	if len(names) > 0 {
		sort.Strings(names)
		return names[0]
	}
	return "application/json"
}

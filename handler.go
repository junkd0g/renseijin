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
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := map[string]any{}
		if raw := req.Params.Arguments; len(raw) > 0 {
			if err := json.Unmarshal(raw, &args); err != nil {
				return errResult(fmt.Errorf("invalid JSON arguments: %w", err)), nil
			}
		}

		httpReq, err := buildHTTPRequest(ctx, op, baseURL, args)
		if err != nil {
			return errResult(err), nil
		}

		resp, err := client.Do(httpReq)
		if err != nil {
			return errResult(fmt.Errorf("http: %w", err)), nil
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return errResult(fmt.Errorf("read body: %w", err)), nil
		}

		text := fmt.Sprintf("HTTP %d %s\n%s", resp.StatusCode, resp.Status, truncateBody(body, maxBytes))
		result := &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: text}},
		}
		if resp.StatusCode >= 400 {
			result.IsError = true
		}
		return result, nil
	}
}

func errResult(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
	}
}

// truncateBody returns body as a string, capped at max bytes. When the cap
// trips, a trailing line tells the model it's looking at a partial view so
// it doesn't silently try to parse a half-JSON document.
func truncateBody(body []byte, max int) string {
	if max <= 0 || len(body) <= max {
		return string(body)
	}
	return string(body[:max]) + fmt.Sprintf("\n... (truncated, %d of %d bytes shown)", max, len(body))
}

// buildHTTPRequest assembles the outbound *http.Request from the operation,
// base URL, and the decoded tool arguments.
func buildHTTPRequest(ctx context.Context, op operation, baseURL string, args map[string]any) (*http.Request, error) {
	pathTmpl := op.pathTmpl
	query := url.Values{}
	header := http.Header{}
	cookies := []*http.Cookie{}

	for _, pref := range op.parameters {
		if pref == nil || pref.Value == nil {
			continue
		}
		p := pref.Value
		raw, present := args[p.Name]
		if !present {
			if p.Required {
				return nil, fmt.Errorf("missing required parameter %q", p.Name)
			}
			continue
		}
		val := stringify(raw)
		switch p.In {
		case openapi3.ParameterInPath:
			pathTmpl = strings.ReplaceAll(pathTmpl, "{"+p.Name+"}", url.PathEscape(val))
		case openapi3.ParameterInQuery:
			query.Set(p.Name, val)
		case openapi3.ParameterInHeader:
			header.Set(p.Name, val)
		case openapi3.ParameterInCookie:
			cookies = append(cookies, &http.Cookie{Name: p.Name, Value: val})
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
	if rb, ok := args["body"]; ok && op.op.RequestBody != nil && op.op.RequestBody.Value != nil {
		mediaType := pickRequestContentType(op.op.RequestBody.Value)
		body, contentType, err = encodeRequestBody(rb, mediaType)
		if err != nil {
			return nil, err
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
//     keys become form fields, values are stringified.
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
			vals.Set(k, stringify(v))
		}
		return strings.NewReader(vals.Encode()), mediaType, nil

	case "multipart/form-data":
		obj, ok := rb.(map[string]any)
		if !ok {
			return nil, "", fmt.Errorf("body for %s must be a JSON object, got %T", mediaType, rb)
		}
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		// Sort keys so the wire bytes are deterministic — easier to test,
		// nicer for any downstream caching.
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
	if obj, ok := v.(map[string]any); ok {
		if fn, ok := obj["filename"].(string); ok && fn != "" {
			enc, _ := obj["content_base64"].(string)
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
	case float64:
		// json.Unmarshal into map[string]any yields float64 for all numbers;
		// strip trailing ".0" so /pets/{petId} doesn't become /pets/42.
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
	if _, ok := rb.Content["application/json"]; ok {
		return "application/json"
	}
	for name := range rb.Content {
		return name
	}
	return "application/json"
}

package renseijin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// makeHandler returns the mcp.ToolHandler that turns a tool invocation into
// an outbound HTTP request and the response into a tool result.
//
// The handler is closed over the spec-derived operation and the caller's
// http.Client.
func makeHandler(op operation, baseURL string, client *http.Client) mcp.ToolHandler {
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

		text := fmt.Sprintf("HTTP %d %s\n%s", resp.StatusCode, resp.Status, string(body))
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
		buf, err := json.Marshal(rb)
		if err != nil {
			return nil, fmt.Errorf("marshal body: %w", err)
		}
		body = bytes.NewReader(buf)
		contentType = pickRequestContentType(op.op.RequestBody.Value)
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

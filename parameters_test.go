package renseijin

import (
	"context"
	"net/url"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/stretchr/testify/require"
)

func TestParameterSerializationStyles(t *testing.T) {
	yes, no := true, false
	array := []any{"a", "b"}
	object := map[string]any{"role": "admin", "first": "Alex"}
	for _, tc := range []struct {
		name, in, style string
		explode         *bool
		value           any
		path            string
		query           url.Values
		header          string
		cookies         map[string]string
	}{
		{name: "query default array", in: "query", value: array, query: url.Values{"id": {"a", "b"}}},
		{name: "query compact array", in: "query", explode: &no, value: array, query: url.Values{"id": {"a,b"}}},
		{name: "query default object", in: "query", value: object, query: url.Values{"first": {"Alex"}, "role": {"admin"}}},
		{name: "query compact object", in: "query", explode: &no, value: object, query: url.Values{"id": {"first,Alex,role,admin"}}},
		{name: "deep object", in: "query", style: "deepObject", explode: &yes, value: object, query: url.Values{"id[first]": {"Alex"}, "id[role]": {"admin"}}},
		{name: "space delimited", in: "query", style: "spaceDelimited", value: array, query: url.Values{"id": {"a b"}}},
		{name: "pipe delimited", in: "query", style: "pipeDelimited", value: array, query: url.Values{"id": {"a|b"}}},
		{name: "query reserved data", in: "query", value: []any{"a&b", "x=y"}, query: url.Values{"id": {"a&b", "x=y"}}},
		{name: "path array", in: "path", value: array, path: "/items/a,b"},
		{name: "path object", in: "path", value: object, path: "/items/first,Alex,role,admin"},
		{name: "path exploded object", in: "path", explode: &yes, value: object, path: "/items/first=Alex,role=admin"},
		{name: "label array", in: "path", style: "label", value: array, path: "/items/.a,b"},
		{name: "label exploded array", in: "path", style: "label", explode: &yes, value: array, path: "/items/.a.b"},
		{name: "label exploded object", in: "path", style: "label", explode: &yes, value: object, path: "/items/.first=Alex.role=admin"},
		{name: "matrix scalar", in: "path", style: "matrix", value: "a", path: "/items/;id=a"},
		{name: "matrix array", in: "path", style: "matrix", value: array, path: "/items/;id=a,b"},
		{name: "matrix exploded array", in: "path", style: "matrix", explode: &yes, value: array, path: "/items/;id=a;id=b"},
		{name: "matrix exploded object", in: "path", style: "matrix", explode: &yes, value: object, path: "/items/;first=Alex;role=admin"},
		{name: "escaped path data", in: "path", value: []any{"a,b", "x/y", "x;y=z"}, path: "/items/a%2Cb,x%2Fy,x%3By%3Dz"},
		{name: "header array", in: "header", value: array, header: "a,b"},
		{name: "header exploded object", in: "header", explode: &yes, value: object, header: "first=Alex,role=admin"},
		{name: "cookie scalar", in: "cookie", value: "a", cookies: map[string]string{"id": "a"}},
		{name: "cookie exploded object", in: "cookie", value: object, cookies: map[string]string{"first": "Alex", "role": "admin"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &openapi3.Parameter{Name: "id", In: tc.in, Style: tc.style, Explode: tc.explode}
			path := "/items"
			if tc.in == "path" {
				path += "/{id}"
			}
			op := operation{method: "GET", pathTmpl: path, op: &openapi3.Operation{}, parameters: openapi3.Parameters{{Value: p}}}
			req, err := buildHTTPRequest(context.Background(), op, "https://example.test", map[string]any{"id": tc.value})
			require.NoError(t, err)
			if tc.path != "" {
				require.Equal(t, tc.path, req.URL.EscapedPath())
			}
			if tc.query != nil {
				require.Equal(t, tc.query, req.URL.Query())
			}
			if tc.header != "" {
				require.Equal(t, tc.header, req.Header.Get("id"))
			}
			for k, v := range tc.cookies {
				c, err := req.Cookie(k)
				require.NoError(t, err)
				require.Equal(t, v, c.Value)
			}
		})
	}
}

func TestUnsupportedParameterEncodingsReturnErrors(t *testing.T) {
	yes := true
	for _, p := range []*openapi3.Parameter{
		{Name: "id", In: "query", Style: "matrix"},
		{Name: "id", In: "query", Style: "pipeDelimited", Explode: &yes},
		{Name: "id", In: "query", AllowReserved: true},
		{Name: "id", In: "query", Content: openapi3.Content{"application/json": &openapi3.MediaType{}}},
	} {
		require.Error(t, checkParameterEncoding(p))
	}
	op := operation{method: "GET", pathTmpl: "/items", op: &openapi3.Operation{}, parameters: openapi3.Parameters{{Value: &openapi3.Parameter{Name: "id", In: "query"}}}}
	_, err := buildHTTPRequest(context.Background(), op, "https://example.test", map[string]any{"id": map[string]any{"nested": map[string]any{"a": "b"}}})
	require.ErrorContains(t, err, "nested parameter values")
}

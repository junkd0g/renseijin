package renseijin

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

func parameterStyle(p *openapi3.Parameter) (string, bool) {
	style := p.Style
	if style == "" {
		if p.In == "query" || p.In == "cookie" {
			style = "form"
		} else {
			style = "simple"
		}
	}
	explode := style == "form"
	if p.Explode != nil {
		explode = *p.Explode
	}
	return style, explode
}

// Reject unsupported encodings explicitly instead of sending JSON strings
// where an API expects a different wire representation.
func checkParameterEncoding(p *openapi3.Parameter) error {
	style, explode := parameterStyle(p)
	valid := false
	switch p.In {
	case "path":
		valid = style == "simple" || style == "label" || style == "matrix"
	case "query":
		valid = style == "form" || ((style == "spaceDelimited" || style == "pipeDelimited") && !explode) || (style == "deepObject" && explode)
	case "header":
		valid = style == "simple"
	case "cookie":
		valid = style == "form"
	}
	if !valid {
		return fmt.Errorf("parameter %q: unsupported %s encoding style=%q explode=%t", p.Name, p.In, style, explode)
	}
	if len(p.Content) > 0 {
		return fmt.Errorf("parameter %q: content-based encoding is not supported", p.Name)
	}
	if p.AllowReserved {
		return fmt.Errorf("parameter %q: allowReserved is not supported", p.Name)
	}
	return nil
}

// Parameter styles describe scalars and flat arrays/objects. Nested structures
// have no defined representation in these styles and must not be guessed.
func parameterParts(v any, escape func(string) string) (kind string, values, keys []string, err error) {
	scalar := func(v any) (string, error) {
		switch v.(type) {
		case []any, map[string]any:
			return "", fmt.Errorf("nested parameter values are not supported")
		}
		return escape(stringify(v)), nil
	}
	switch v := v.(type) {
	case []any:
		kind = "array"
		for _, item := range v {
			s, e := scalar(item)
			if e != nil {
				return "", nil, nil, e
			}
			values = append(values, s)
		}
	case map[string]any:
		kind = "object"
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for i, k := range keys {
			s, e := scalar(v[k])
			if e != nil {
				return "", nil, nil, e
			}
			values = append(values, s)
			keys[i] = escape(k)
		}
	default:
		kind = "scalar"
		s, e := scalar(v)
		if e != nil {
			return "", nil, nil, e
		}
		values = []string{s}
	}
	return kind, values, keys, nil
}

func joinedParts(kind string, values, keys []string, explode bool, sep string) string {
	if kind != "object" {
		return strings.Join(values, sep)
	}
	parts := make([]string, 0, len(keys)*2)
	for i, k := range keys {
		if explode {
			parts = append(parts, k+"="+values[i])
		} else {
			parts = append(parts, k, values[i])
		}
	}
	return strings.Join(parts, sep)
}

func serializeParameter(p *openapi3.Parameter, raw any, path *string, query url.Values, headers http.Header, cookies *[]*http.Cookie) error {
	if err := checkParameterEncoding(p); err != nil {
		return err
	}
	style, explode := parameterStyle(p)
	escape := func(s string) string { return s }
	if p.In == "path" {
		// Escape data separately from style delimiters (including commas and
		// semicolons, which PathEscape would otherwise leave unescaped).
		escape = func(s string) string { return strings.ReplaceAll(url.QueryEscape(s), "+", "%20") }
	}
	kind, values, keys, err := parameterParts(raw, escape)
	if err != nil {
		return fmt.Errorf("parameter %q: %w", p.Name, err)
	}
	switch p.In {
	case "path":
		value := joinedParts(kind, values, keys, explode, ",")
		switch style {
		case "label":
			sep := ","
			if explode {
				sep = "."
			}
			value = "." + joinedParts(kind, values, keys, explode, sep)
		case "matrix":
			name := escape(p.Name)
			if explode && kind == "array" {
				value = ""
				for _, v := range values {
					value += ";" + name + "=" + v
				}
			} else if explode && kind == "object" {
				value = ""
				for i, k := range keys {
					value += ";" + k + "=" + values[i]
				}
			} else {
				value = ";" + name + "=" + value
			}
		}
		*path = strings.ReplaceAll(*path, "{"+p.Name+"}", value)
	case "header":
		headers.Set(p.Name, joinedParts(kind, values, keys, explode, ","))
	case "query", "cookie":
		add := func(k, v string) {
			if p.In == "query" {
				query.Add(k, v)
			} else {
				*cookies = append(*cookies, &http.Cookie{Name: k, Value: url.QueryEscape(v)})
			}
		}
		switch style {
		case "deepObject":
			if kind != "object" {
				return fmt.Errorf("parameter %q: deepObject requires an object", p.Name)
			}
			for i, k := range keys {
				add(p.Name+"["+k+"]", values[i])
			}
		case "spaceDelimited", "pipeDelimited":
			if kind != "array" {
				return fmt.Errorf("parameter %q: %s requires an array", p.Name, style)
			}
			sep := " "
			if style == "pipeDelimited" {
				sep = "|"
			}
			add(p.Name, strings.Join(values, sep))
		default:
			if explode && kind == "array" {
				for _, v := range values {
					add(p.Name, v)
				}
			} else if explode && kind == "object" {
				for i, k := range keys {
					add(k, values[i])
				}
			} else {
				add(p.Name, joinedParts(kind, values, keys, false, ","))
			}
		}
	}
	return nil
}

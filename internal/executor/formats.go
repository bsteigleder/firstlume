package executor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/bsteigleder/firstlume/internal/stackconfig"
)

// Rendering of declared files (spec 0003, R2.2). Values reach here resolved:
// only scalars, maps and lists, every source replaced by a text scalar.

func renderFile(f *stackconfig.FileSpec, values *stackconfig.Value) ([]byte, error) {
	switch f.Format {
	case stackconfig.FormatHCL:
		return renderHCL(values, f.Block), nil
	case stackconfig.FormatJSON:
		var b bytes.Buffer
		writeJSON(&b, values, "")
		b.WriteByte('\n')
		return b.Bytes(), nil
	case stackconfig.FormatYAML:
		var b bytes.Buffer
		enc := yaml.NewEncoder(&b)
		enc.SetIndent(2)
		if err := enc.Encode(yamlNode(values)); err != nil {
			return nil, err
		}
		return b.Bytes(), enc.Close()
	case stackconfig.FormatEnv:
		var b bytes.Buffer
		for _, e := range values.Map {
			fmt.Fprintf(&b, "%s=%s\n", e.Key, shellQuote(scalarString(e.Value)))
		}
		return b.Bytes(), nil
	case stackconfig.FormatRaw:
		return []byte(scalarString(values)), nil
	}
	return nil, fmt.Errorf("unknown format %q", f.Format)
}

// scalarString is a scalar's text; null is empty.
func scalarString(v *stackconfig.Value) string {
	if v.Tag == "!!null" {
		return ""
	}
	return v.Text
}

var hclIdent = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

func renderHCL(values *stackconfig.Value, block string) []byte {
	var b bytes.Buffer
	indent := ""
	if block != "" {
		fmt.Fprintf(&b, "%s {\n", block)
		indent = "  "
	}
	for _, e := range values.Map {
		fmt.Fprintf(&b, "%s%s = ", indent, e.Key)
		writeHCL(&b, e.Value, indent)
		b.WriteByte('\n')
	}
	if block != "" {
		b.WriteString("}\n")
	}
	return b.Bytes()
}

func writeHCL(b *bytes.Buffer, v *stackconfig.Value, indent string) {
	inner := indent + "  "
	switch v.Kind {
	case stackconfig.Map:
		if len(v.Map) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteString("{\n")
		for _, e := range v.Map {
			key := e.Key
			if !hclIdent.MatchString(key) {
				key = hclString(key)
			}
			fmt.Fprintf(b, "%s%s = ", inner, key)
			writeHCL(b, e.Value, inner)
			b.WriteByte('\n')
		}
		b.WriteString(indent + "}")
	case stackconfig.List:
		if len(v.List) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteString("[\n")
		for _, c := range v.List {
			b.WriteString(inner)
			writeHCL(b, c, inner)
			b.WriteString(",\n")
		}
		b.WriteString(indent + "]")
	default:
		switch v.Tag {
		case "!!str":
			b.WriteString(hclString(v.Text))
		case "!!null":
			b.WriteString("null")
		default: // numbers and booleans, already normalized
			b.WriteString(v.Text)
		}
	}
}

// hclString quotes s as an HCL string literal. Template sequences are escaped,
// so a value is never interpreted by Terraform.
func hclString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i, r := range s {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case r == '"':
			b.WriteString(`\"`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04x`, r)
		case (r == '$' || r == '%') && strings.HasPrefix(s[i+1:], "{"):
			b.WriteRune(r)
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// writeJSON writes v keeping the declaration's key order.
func writeJSON(b *bytes.Buffer, v *stackconfig.Value, indent string) {
	inner := indent + "  "
	switch v.Kind {
	case stackconfig.Map:
		if len(v.Map) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteString("{\n")
		for i, e := range v.Map {
			key, _ := json.Marshal(e.Key)
			fmt.Fprintf(b, "%s%s: ", inner, key)
			writeJSON(b, e.Value, inner)
			if i < len(v.Map)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(indent + "}")
	case stackconfig.List:
		if len(v.List) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteString("[\n")
		for i, c := range v.List {
			b.WriteString(inner)
			writeJSON(b, c, inner)
			if i < len(v.List)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(indent + "]")
	default:
		switch v.Tag {
		case "!!str":
			s, _ := json.Marshal(v.Text)
			b.Write(s)
		case "!!null":
			b.WriteString("null")
		default:
			b.WriteString(v.Text)
		}
	}
}

func yamlNode(v *stackconfig.Value) *yaml.Node {
	switch v.Kind {
	case stackconfig.Map:
		n := &yaml.Node{Kind: yaml.MappingNode}
		for _, e := range v.Map {
			n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: e.Key, Style: yaml.DoubleQuotedStyle},
				yamlNode(e.Value))
		}
		return n
	case stackconfig.List:
		n := &yaml.Node{Kind: yaml.SequenceNode}
		for _, c := range v.List {
			n.Content = append(n.Content, yamlNode(c))
		}
		return n
	}
	n := &yaml.Node{Kind: yaml.ScalarNode, Tag: v.Tag, Value: v.Text}
	if v.Tag == "!!str" {
		// Always quoted: a value like "yes", "0x10" or "null" stays a string.
		n.Style = yaml.DoubleQuotedStyle
	}
	if v.Tag == "!!null" {
		n.Value = "null"
	}
	return n
}

// shellQuote quotes s for a shell's `source` (and most .env readers).
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

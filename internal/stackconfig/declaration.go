package stackconfig

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/bsteigleder/firstlume/internal/api"
)

// The declaration (spec 0003) says what a run needs besides the code: files
// generated in the stack's directory, environment and Terraform variables and
// registry credentials, each value a literal or a reference to where it comes
// from.

// Declaration is the parsed declaration, values included.
type Declaration struct {
	Files      []FileSpec
	Env        []Variable
	Terraform  []Variable
	Registries []Registry
}

func (d *Declaration) empty() bool {
	return len(d.Files) == 0 && len(d.Env) == 0 && len(d.Terraform) == 0 && len(d.Registries) == 0
}

// Formats a declared file can be written in.
const (
	FormatHCL  = "hcl"
	FormatJSON = "json"
	FormatYAML = "yaml"
	FormatEnv  = "env"
	FormatRaw  = "raw"
)

// FileSpec is a file the run writes, relative to the stack's directory. Its
// directory may be a pattern: then the file is written in every directory
// that matches (R2.3).
type FileSpec struct {
	Line   int
	Path   string
	Format string
	// Block wraps an hcl file's attributes, as in `locals { ... }`.
	Block string
	// Values holds the attributes of every format but raw, whose content is Content.
	Values  *Value
	Content *Value
	// Sensitive is nil when not declared: then the sources decide.
	Sensitive *bool
}

// Variable is an environment or Terraform variable.
type Variable struct {
	Line  int
	Name  string
	Value *Value
}

// Registry is a registry credential (spec 0003, R4).
type Registry struct {
	Line     int
	Host     string
	Kind     string
	Username *Value
	Password *Value
}

// ValueKind tells what a Value is.
type ValueKind int

const (
	Scalar ValueKind = iota
	Map
	List
	// VarRef is a variable from the stack's settings.
	VarRef
	// VaultRef is a field of a Vault secret.
	VaultRef
)

// Value is a value of the declaration.
type Value struct {
	Line int
	Kind ValueKind
	// Tag and Text describe a scalar: Tag is !!str, !!int, !!float, !!bool or !!null.
	Tag  string
	Text string
	Map  []Entry
	List []*Value
	// Var names the variable of a VarRef.
	Var string
	// Path and Field locate a VaultRef.
	Path  string
	Field string
}

type Entry struct {
	Key   string
	Value *Value
}

var (
	envNameRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,254}$`)
	tfNameRe   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,254}$`)
	hclIdentRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)
	hostRe     = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,252}[A-Za-z0-9])?(:[0-9]{1,5})?$`)
)

const (
	maxPathLen    = 512
	maxValueDepth = 32
)

// ReservedEnv reports whether an environment variable belongs to Firstlume or
// the tools it runs, so neither settings nor a declaration may set it.
func ReservedEnv(key string) bool {
	upper := strings.ToUpper(key)
	switch upper {
	case "PATH", "HOME", "TF_IN_AUTOMATION", "TF_INPUT", "TF_CLI_CONFIG_FILE", "TF_DATA_DIR", "TF_PLUGIN_CACHE_DIR",
		"DOCKER_CONFIG", "HELM_REGISTRY_CONFIG":
		return true
	}
	return strings.HasPrefix(upper, "FIRSTLUME_") || strings.HasPrefix(upper, "GIT_") || strings.HasPrefix(upper, "TF_HTTP_")
}

// ValidFilePath checks a file's path relative to the stack's directory (R2.6).
// Patterns are allowed in the directory part only when patterns is true.
func ValidFilePath(p string, patterns bool) error {
	switch {
	case p == "" || len(p) > maxPathLen:
		return fmt.Errorf("the path must have 1 to %d characters", maxPathLen)
	case strings.HasPrefix(p, "/") || strings.Contains(p, `\`):
		return fmt.Errorf("the path %q must be relative, with / between directories", p)
	case strings.ContainsFunc(p, func(r rune) bool { return r < 0x20 || r == 0x7f }):
		return fmt.Errorf("the path %q has control characters", p)
	}
	segs := strings.Split(p, "/")
	for i, s := range segs {
		switch {
		case s == "" || s == ".":
			return fmt.Errorf("the path %q has an empty or '.' segment", p)
		case s == "..":
			return fmt.Errorf("the path %q must not use ..", p)
		case s == ".git" || s == Dir:
			return fmt.Errorf("the path %q must not be inside %s", p, s)
		case strings.Contains(s, "**"):
			return fmt.Errorf("the path %q: ** is not supported, use one * per directory level", p)
		}
		if hasPattern(s) {
			if !patterns || i == len(segs)-1 {
				return fmt.Errorf("the path %q: patterns are allowed only in the directories, not in the file name", p)
			}
			if _, err := path.Match(s, ""); err != nil {
				return fmt.Errorf("the path %q has an invalid pattern %q", p, s)
			}
		}
	}
	return nil
}

func hasPattern(s string) bool { return strings.ContainsAny(s, "*?[") }

func lineErr(n *yaml.Node, format string, args ...any) error {
	return fmt.Errorf("line %d: %s", n.Line, fmt.Sprintf(format, args...))
}

func scalarText(n *yaml.Node) (string, error) {
	if n.Kind != yaml.ScalarNode {
		return "", lineErr(n, "expected a single value")
	}
	return n.Value, nil
}

// pairs walks a mapping node, rejecting keys not in allowed.
func pairs(n *yaml.Node, what string, allowed []string, each func(key string, k, v *yaml.Node) error) error {
	if n.Kind != yaml.MappingNode {
		return lineErr(n, "%s must be a mapping", what)
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if k.Kind != yaml.ScalarNode {
			return lineErr(k, "%s: keys must be single values", what)
		}
		if allowed != nil && !contains(allowed, k.Value) {
			return lineErr(k, "%s: unknown key %q (expected %s)", what, k.Value, strings.Join(allowed, ", "))
		}
		if err := each(k.Value, k, v); err != nil {
			return err
		}
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func parseFiles(n *yaml.Node) ([]FileSpec, error) {
	if n.Kind != yaml.SequenceNode {
		return nil, lineErr(n, "files must be a list")
	}
	var out []FileSpec
	for _, item := range n.Content {
		f := FileSpec{Line: item.Line}
		err := pairs(item, "file", []string{"path", "format", "block", "values", "content", "sensitive"}, func(key string, k, v *yaml.Node) error {
			var err error
			switch key {
			case "path":
				f.Path, err = scalarText(v)
			case "format":
				f.Format, err = scalarText(v)
			case "block":
				f.Block, err = scalarText(v)
			case "values":
				f.Values, err = parseValue(v, 0)
			case "content":
				f.Content, err = parseValue(v, 0)
			case "sensitive":
				var b bool
				err = v.Decode(&b)
				f.Sensitive = &b
			}
			return err
		})
		if err != nil {
			return nil, err
		}
		if err := checkFile(item, &f); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}

func checkFile(n *yaml.Node, f *FileSpec) error {
	if f.Path == "" {
		return lineErr(n, "a file needs a path")
	}
	if err := ValidFilePath(f.Path, true); err != nil {
		return lineErr(n, "%v", err)
	}
	switch f.Format {
	case FormatHCL, FormatJSON, FormatYAML, FormatEnv:
		if f.Content != nil {
			return lineErr(n, "%s: content is only for the raw format; use values", f.Path)
		}
		if f.Values == nil || f.Values.Kind != Map {
			return lineErr(n, "%s: values must be a mapping", f.Path)
		}
	case FormatRaw:
		if f.Values != nil {
			return lineErr(n, "%s: the raw format takes content, not values", f.Path)
		}
		if f.Content == nil || f.Content.Kind == Map || f.Content.Kind == List {
			return lineErr(n, "%s: content must be a single value or a source", f.Path)
		}
	case "":
		return lineErr(n, "%s: format is required (hcl, json, yaml, env or raw)", f.Path)
	default:
		return lineErr(n, "%s: unknown format %q (expected hcl, json, yaml, env or raw)", f.Path, f.Format)
	}
	if f.Block != "" && f.Format != FormatHCL {
		return lineErr(n, "%s: block is only for the hcl format", f.Path)
	}
	if f.Block != "" && !hclIdentRe.MatchString(f.Block) {
		return lineErr(n, "%s: block %q is not an HCL name", f.Path, f.Block)
	}
	for _, e := range mapEntries(f.Values) {
		switch {
		case f.Format == FormatHCL && !hclIdentRe.MatchString(e.Key):
			return lineErr(n, "%s: %q is not an HCL attribute name", f.Path, e.Key)
		case f.Format == FormatEnv && !envNameRe.MatchString(e.Key):
			return lineErr(n, "%s: %q is not an environment variable name", f.Path, e.Key)
		case f.Format == FormatEnv && (e.Value.Kind == Map || e.Value.Kind == List):
			return lineErr(n, "%s: %s must be a single value in the env format", f.Path, e.Key)
		}
	}
	return nil
}

func mapEntries(v *Value) []Entry {
	if v == nil {
		return nil
	}
	return v.Map
}

func parseVariables(n *yaml.Node, kind string) ([]Variable, error) {
	var out []Variable
	err := pairs(n, kind, nil, func(name string, k, v *yaml.Node) error {
		switch {
		case kind == "env" && !envNameRe.MatchString(name):
			return lineErr(k, "%q is not an environment variable name", name)
		case kind == "env" && ReservedEnv(name):
			return lineErr(k, "the environment variable %q is reserved", name)
		case kind == "terraform" && !tfNameRe.MatchString(name):
			return lineErr(k, "%q is not a Terraform variable name", name)
		}
		val, err := parseValue(v, 0)
		if err != nil {
			return err
		}
		if kind == "env" && (val.Kind == Map || val.Kind == List) {
			return lineErr(v, "env %s must be a single value or a source", name)
		}
		out = append(out, Variable{Line: k.Line, Name: name, Value: val})
		return nil
	})
	return out, err
}

func parseRegistries(n *yaml.Node) ([]Registry, error) {
	if n.Kind != yaml.SequenceNode {
		return nil, lineErr(n, "registries must be a list")
	}
	var out []Registry
	for _, item := range n.Content {
		r := Registry{Line: item.Line, Kind: "oci"}
		err := pairs(item, "registry", []string{"host", "kind", "username", "password"}, func(key string, k, v *yaml.Node) error {
			var err error
			switch key {
			case "host":
				r.Host, err = scalarText(v)
			case "kind":
				r.Kind, err = scalarText(v)
			case "username":
				r.Username, err = parseValue(v, 0)
			case "password":
				r.Password, err = parseValue(v, 0)
			}
			return err
		})
		if err != nil {
			return nil, err
		}
		switch {
		case !hostRe.MatchString(r.Host):
			return nil, lineErr(item, "registry host %q must be a host name, optionally with a port", r.Host)
		case r.Kind != "oci" && r.Kind != "terraform":
			return nil, lineErr(item, "registry %s: kind must be oci or terraform", r.Host)
		case r.Kind == "oci" && r.Username == nil:
			return nil, lineErr(item, "registry %s: username is required", r.Host)
		case r.Kind == "terraform" && r.Username != nil:
			return nil, lineErr(item, "registry %s: a terraform registry takes only a token, as password", r.Host)
		case r.Password == nil:
			return nil, lineErr(item, "registry %s: password is required", r.Host)
		}
		for _, v := range []*Value{r.Username, r.Password} {
			if v != nil && (v.Kind == Map || v.Kind == List) {
				return nil, lineErr(item, "registry %s: username and password must be single values or sources", r.Host)
			}
		}
		out = append(out, r)
	}
	return out, nil
}

// parseValue reads a value. A mapping with the key var or vault is a source.
func parseValue(n *yaml.Node, depth int) (*Value, error) {
	if depth > maxValueDepth {
		return nil, lineErr(n, "values nest too deep")
	}
	v := &Value{Line: n.Line}
	switch n.Kind {
	case yaml.AliasNode:
		return parseValue(n.Alias, depth+1)
	case yaml.ScalarNode:
		v.Kind, v.Tag, v.Text = Scalar, n.ShortTag(), n.Value
		switch v.Tag {
		case "!!str", "!!bool", "!!null":
		case "!!timestamp":
			v.Tag = "!!str"
		case "!!int":
			var i int64
			if err := n.Decode(&i); err != nil {
				return nil, lineErr(n, "%q is not a 64-bit integer", n.Value)
			}
			v.Text = fmt.Sprint(i)
		case "!!float":
			var f float64
			if err := n.Decode(&f); err != nil || f != f || f > 1.7e308 || f < -1.7e308 {
				return nil, lineErr(n, "%q is not a finite number", n.Value)
			}
		default:
			return nil, lineErr(n, "unsupported value type %s", v.Tag)
		}
		if v.Tag == "!!bool" {
			var b bool
			_ = n.Decode(&b)
			v.Text = fmt.Sprint(b)
		}
		return v, nil
	case yaml.SequenceNode:
		v.Kind = List
		for _, item := range n.Content {
			c, err := parseValue(item, depth+1)
			if err != nil {
				return nil, err
			}
			v.List = append(v.List, c)
		}
		return v, nil
	case yaml.MappingNode:
		if isSource(n) {
			return parseSource(n)
		}
		v.Kind = Map
		err := pairs(n, "values", nil, func(key string, k, val *yaml.Node) error {
			c, err := parseValue(val, depth+1)
			if err == nil {
				v.Map = append(v.Map, Entry{Key: key, Value: c})
			}
			return err
		})
		return v, err
	}
	return nil, lineErr(n, "unsupported value")
}

func isSource(n *yaml.Node) bool {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if k := n.Content[i].Value; k == "var" || k == "vault" {
			return true
		}
	}
	return false
}

func parseSource(n *yaml.Node) (*Value, error) {
	v := &Value{Line: n.Line}
	fields := map[string]string{}
	err := pairs(n, "source", []string{"var", "vault", "field"}, func(key string, k, val *yaml.Node) error {
		if val.Kind != yaml.ScalarNode || val.ShortTag() != "!!str" || val.Value == "" {
			return lineErr(val, "%s must be a non-empty text", key)
		}
		fields[key] = val.Value
		return nil
	})
	if err != nil {
		return nil, err
	}
	switch {
	case fields["var"] != "" && len(fields) == 1:
		if !tfNameRe.MatchString(fields["var"]) {
			return nil, lineErr(n, "%q is not a variable name", fields["var"])
		}
		v.Kind, v.Var = VarRef, fields["var"]
	case fields["vault"] != "" && fields["field"] != "" && len(fields) == 2:
		p := fields["vault"]
		if strings.HasPrefix(p, "/") || strings.Contains(p, "//") || strings.HasSuffix(p, "/") {
			return nil, lineErr(n, "vault path %q must look like mount/path/to/secret", p)
		}
		for _, seg := range strings.Split(p, "/") {
			if seg == ".." || seg == "." {
				return nil, lineErr(n, "vault path %q must not use . or ..", p)
			}
		}
		v.Kind, v.Path, v.Field = VaultRef, p, fields["field"]
	default:
		return nil, lineErr(n, "a source is {var: NAME} or {vault: path, field: name}")
	}
	return v, nil
}

// Ref describes where a source comes from: "var:NAME" or "vault:path#field".
func (v *Value) Ref() string {
	switch v.Kind {
	case VarRef:
		return "var:" + v.Var
	case VaultRef:
		return "vault:" + v.Path + "#" + v.Field
	}
	return ""
}

// Refs lists the sources in v, in order, without repeats.
func (v *Value) Refs() []string {
	var out []string
	seen := map[string]bool{}
	var walk func(*Value)
	walk = func(v *Value) {
		if v == nil {
			return
		}
		if r := v.Ref(); r != "" && !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
		for _, e := range v.Map {
			walk(e.Value)
		}
		for _, c := range v.List {
			walk(c)
		}
	}
	walk(v)
	return out
}

// UsesVault reports whether any source in v is a Vault secret.
func (v *Value) UsesVault() bool {
	for _, r := range v.Refs() {
		if strings.HasPrefix(r, "vault:") {
			return true
		}
	}
	return false
}

// Summary describes the declaration without values.
func (d *Declaration) Summary() *api.Declaration {
	s := &api.Declaration{}
	for _, f := range d.Files {
		v := f.Values
		if v == nil {
			v = f.Content
		}
		sensitive := v.UsesVault()
		if f.Sensitive != nil {
			sensitive = *f.Sensitive
		}
		s.Files = append(s.Files, api.DeclaredFile{Path: f.Path, Format: f.Format, Block: f.Block, Sensitive: sensitive, Refs: v.Refs()})
	}
	for _, v := range d.Env {
		s.Env = append(s.Env, api.DeclaredVariable{Name: v.Name, Ref: v.Value.Ref()})
	}
	for _, v := range d.Terraform {
		s.Terraform = append(s.Terraform, api.DeclaredVariable{Name: v.Name, Ref: v.Value.Ref()})
	}
	for _, r := range d.Registries {
		refs := r.Password.Refs()
		if r.Username != nil {
			refs = append(r.Username.Refs(), refs...)
		}
		s.Registries = append(s.Registries, api.DeclaredRegistry{Host: r.Host, Kind: r.Kind, Refs: refs})
	}
	return s
}

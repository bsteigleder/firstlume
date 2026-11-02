package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bsteigleder/firstlume/internal/api"
	"github.com/bsteigleder/firstlume/internal/stackconfig"
)

// Files and variables declared in .firstlume/config.yaml and file variables
// from the stack's settings (spec 0003): written into the stack's directory
// after the checkout and before init.

const (
	maxPreparedFiles = 1000
	maxPreparedFile  = 256 << 10
	maxPreparedTotal = 16 << 20
)

// prepared is what preparing a phase produced.
type prepared struct {
	// files maps each written file, relative to the stack's directory, to what wrote it.
	files map[string]string
	// env holds the declared environment and Terraform variables.
	env []string
}

type outputFile struct {
	rel       string
	origin    string
	content   []byte
	sensitive bool
}

// prepare writes the declared files and the file variables into dir, the
// stack's directory, and the registry credentials into home, the phase's
// HOME. It returns the environment of the declared variables and of the
// registries. Settings win over the declaration for the same variable or
// registry host (D6). Secrets it handles are added to masker.
func prepare(ctx context.Context, dir, home string, decl *stackconfig.Declaration, vars []api.ExecVariable,
	registries []api.ExecRegistry, vault *vaultSource, masker *Masker, out io.Writer) (*prepared, error) {
	stackDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	r := &resolver{ctx: ctx, vars: vars, vault: vault, stackDir: stackDir}
	if decl == nil {
		decl = &stackconfig.Declaration{}
	}
	var files []outputFile
	for i := range decl.Files {
		out, err := r.declaredFile(dir, &decl.Files[i])
		if err != nil {
			return nil, err
		}
		files = append(files, out...)
	}
	for _, v := range vars {
		if v.Category == api.CategoryFile {
			files = append(files, outputFile{rel: v.Key, origin: "file variable " + v.Key, content: []byte(v.Value), sensitive: v.Sensitive})
		}
	}
	if err := writeFiles(dir, files); err != nil {
		return nil, err
	}
	p := &prepared{files: map[string]string{}}
	for _, f := range files {
		p.files[f.rel] = f.origin
		if f.sensitive {
			masker.Add(string(f.content))
		}
		fmt.Fprintf(out, "==> Wrote %s (%s%s)\n", f.rel, f.origin, sensitiveNote(f.sensitive))
	}

	inSettings := map[string]bool{}
	for _, v := range vars {
		inSettings[string(v.Category)+"/"+v.Key] = true
	}
	for _, group := range []struct {
		category api.VariableCategory
		vars     []stackconfig.Variable
		prefix   string
	}{{api.CategoryEnv, decl.Env, ""}, {api.CategoryTerraform, decl.Terraform, "TF_VAR_"}} {
		for _, v := range group.vars {
			if inSettings[string(group.category)+"/"+v.Name] {
				fmt.Fprintf(out, "==> %s %s is also set in the stack's settings, which win\n", group.category, v.Name)
				continue
			}
			res, err := r.resolve(v.Value, dirVars{stackDir: r.stackDir})
			if err != nil {
				return nil, fmt.Errorf("line %d: %s %s: %w", v.Line, group.category, v.Name, err)
			}
			if res.Kind == stackconfig.Scalar && res.Tag == "!!null" {
				continue // unset: Terraform uses the variable's default
			}
			value := scalarString(res)
			if res.Kind != stackconfig.Scalar {
				value = compactJSON(res)
			}
			if r.sensitive {
				masker.Add(value)
			}
			p.env = append(p.env, group.prefix+v.Name+"="+value)
		}
	}
	regEnv, err := r.registries(home, registries, decl.Registries, masker, out)
	if err != nil {
		return nil, err
	}
	p.env = append(p.env, regEnv...)
	return p, nil
}

func sensitiveNote(sensitive bool) string {
	if sensitive {
		return ", sensitive"
	}
	return ""
}

// dirVars are the ${dir} and ${name} of a file written in a matched directory,
// and ${stack_dir}, the stack's absolute directory in this run: code that runs
// inside .terragrunt-cache needs it to find a file the run wrote.
type dirVars struct{ dir, name, stackDir string }

// expand replaces ${dir}, ${name} and ${stack_dir}; $${ is a literal ${.
func (d dirVars) expand(s string) string {
	if !strings.Contains(s, "${") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		switch rest := s[i:]; {
		case strings.HasPrefix(rest, "$${"):
			b.WriteString("${")
			i += 3
		case strings.HasPrefix(rest, "${dir}"):
			b.WriteString(d.dir)
			i += len("${dir}")
		case strings.HasPrefix(rest, "${name}"):
			b.WriteString(d.name)
			i += len("${name}")
		case strings.HasPrefix(rest, "${stack_dir}"):
			b.WriteString(d.stackDir)
			i += len("${stack_dir}")
		default:
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String()
}

// resolver turns declared values into plain ones.
type resolver struct {
	ctx      context.Context
	vars     []api.ExecVariable
	vault    *vaultSource
	stackDir string
	// sensitive is set when the last resolve used a Vault secret or a sensitive variable.
	sensitive bool
}

func (r *resolver) resolve(v *stackconfig.Value, d dirVars) (*stackconfig.Value, error) {
	r.sensitive = false
	return r.walk(v, d)
}

func (r *resolver) walk(v *stackconfig.Value, d dirVars) (*stackconfig.Value, error) {
	switch v.Kind {
	case stackconfig.Scalar:
		if v.Tag == "!!str" {
			c := *v
			c.Text = d.expand(v.Text)
			return &c, nil
		}
		return v, nil
	case stackconfig.Map:
		c := &stackconfig.Value{Line: v.Line, Kind: stackconfig.Map}
		for _, e := range v.Map {
			rv, err := r.walk(e.Value, d)
			if err != nil {
				return nil, err
			}
			c.Map = append(c.Map, stackconfig.Entry{Key: e.Key, Value: rv})
		}
		return c, nil
	case stackconfig.List:
		c := &stackconfig.Value{Line: v.Line, Kind: stackconfig.List}
		for _, item := range v.List {
			rv, err := r.walk(item, d)
			if err != nil {
				return nil, err
			}
			c.List = append(c.List, rv)
		}
		return c, nil
	case stackconfig.VarRef:
		val, err := r.variable(v.Var)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", v.Line, err)
		}
		if val.Sensitive {
			r.sensitive = true
		}
		return &stackconfig.Value{Line: v.Line, Kind: stackconfig.Scalar, Tag: "!!str", Text: val.Value}, nil
	case stackconfig.VaultRef:
		path := d.expand(v.Path)
		val, err := r.vault.read(r.ctx, path, v.Field)
		if err != nil {
			return nil, fmt.Errorf("line %d: vault %s#%s: %w", v.Line, path, v.Field, err)
		}
		r.sensitive = true
		return &stackconfig.Value{Line: v.Line, Kind: stackconfig.Scalar, Tag: "!!str", Text: val}, nil
	}
	return nil, fmt.Errorf("line %d: unsupported value", v.Line)
}

// variable finds a variable of the stack's settings for {var: NAME}.
func (r *resolver) variable(name string) (*api.ExecVariable, error) {
	var found *api.ExecVariable
	for i, v := range r.vars {
		if v.Key != name || v.Category == api.CategoryFile {
			continue
		}
		if found != nil && found.Value != v.Value {
			return nil, fmt.Errorf("variable %s is set both as env and as terraform, with different values", name)
		}
		found = &r.vars[i]
	}
	if found == nil {
		return nil, fmt.Errorf("variable %s is not set in the stack's settings", name)
	}
	return found, nil
}

// declaredFile renders a declared file in each directory it goes to.
func (r *resolver) declaredFile(dir string, f *stackconfig.FileSpec) ([]outputFile, error) {
	dirPattern, base := path.Split(f.Path)
	dirPattern = strings.TrimSuffix(dirPattern, "/")
	var dirs []string
	if strings.ContainsAny(dirPattern, "*?[") {
		var err error
		if dirs, err = matchDirs(dir, dirPattern); err != nil {
			return nil, fmt.Errorf("line %d: %s: %w", f.Line, f.Path, err)
		}
		if len(dirs) == 0 {
			return nil, fmt.Errorf("line %d: %s: no directory matches %s", f.Line, f.Path, dirPattern)
		}
	} else {
		dirs = []string{dirPattern}
	}
	source := f.Values
	if source == nil {
		source = f.Content
	}
	var out []outputFile
	for _, d := range dirs {
		vars := dirVars{dir: d, name: path.Base(d), stackDir: r.stackDir}
		if d == "" {
			vars.name = ""
		}
		values, err := r.resolve(source, vars)
		if err != nil {
			return nil, fmt.Errorf("line %d: %s: %w", f.Line, f.Path, err)
		}
		sensitive := r.sensitive
		if f.Sensitive != nil {
			sensitive = *f.Sensitive
		}
		content, err := renderFile(f, values)
		if err != nil {
			return nil, fmt.Errorf("line %d: %s: %w", f.Line, f.Path, err)
		}
		name := vars.expand(base)
		if name == "" || strings.Contains(name, "/") || name == "." || name == ".." {
			return nil, fmt.Errorf("line %d: %s: the file name %q is not valid in %s", f.Line, f.Path, name, d)
		}
		out = append(out, outputFile{rel: path.Join(d, name), origin: fmt.Sprintf("%s line %d", stackconfig.File, f.Line),
			content: content, sensitive: sensitive})
	}
	return out, nil
}

// matchDirs returns the directories below root that match pattern. Hidden
// directories (.terraform, .terragrunt-cache) match only a pattern that names
// them with their dot.
func matchDirs(root, pattern string) ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(pattern)))
	if err != nil {
		return nil, err
	}
	patSegs := strings.Split(pattern, "/")
	var out []string
	for _, m := range matches {
		rel, err := filepath.Rel(root, m)
		if err != nil {
			return nil, err
		}
		rel = filepath.ToSlash(rel)
		if info, err := os.Stat(m); err != nil || !info.IsDir() {
			continue
		}
		hidden := false
		for i, seg := range strings.Split(rel, "/") {
			if strings.HasPrefix(seg, ".") && (i >= len(patSegs) || !strings.HasPrefix(patSegs[i], ".")) {
				hidden = true
			}
		}
		if !hidden {
			out = append(out, rel)
		}
	}
	sort.Strings(out)
	return out, nil
}

// writeFiles writes files into root after checking them all: limits, no file
// written twice, none that exists already (it would replace code from the
// repository) and none that a symbolic link would put outside root.
func writeFiles(root string, files []outputFile) error {
	if len(files) == 0 {
		return nil
	}
	if len(files) > maxPreparedFiles {
		return fmt.Errorf("%d files to write, more than the limit of %d", len(files), maxPreparedFiles)
	}
	total := 0
	seen := map[string]string{}
	for _, f := range files {
		if other, ok := seen[f.rel]; ok {
			return fmt.Errorf("%s is written by both %s and %s", f.rel, other, f.origin)
		}
		seen[f.rel] = f.origin
		if len(f.content) > maxPreparedFile {
			return fmt.Errorf("%s (%s) has %d bytes, more than the limit of %d KiB", f.rel, f.origin, len(f.content), maxPreparedFile>>10)
		}
		total += len(f.content)
	}
	if total > maxPreparedTotal {
		return fmt.Errorf("the files to write add up to %d bytes, more than the limit of %d MiB", total, maxPreparedTotal>>20)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	for _, f := range files {
		target := filepath.Join(root, filepath.FromSlash(f.rel))
		if _, err := os.Lstat(target); err == nil {
			return fmt.Errorf("%s (%s) already exists in the repository: First Lume doesn't replace versioned files", f.rel, f.origin)
		}
		if err := insideRoot(realRoot, filepath.Dir(target)); err != nil {
			return fmt.Errorf("%s (%s): %w", f.rel, f.origin, err)
		}
	}
	for _, f := range files {
		target := filepath.Join(root, filepath.FromSlash(f.rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if f.sensitive {
			mode = 0o600
		}
		// O_EXCL never follows a link nor replaces a file.
		fh, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			return fmt.Errorf("writing %s: %w", f.rel, err)
		}
		_, err = fh.Write(f.content)
		if cerr := fh.Close(); err == nil {
			err = cerr
		}
		if err == nil {
			err = os.Chmod(target, mode)
		}
		if err != nil {
			return fmt.Errorf("writing %s: %w", f.rel, err)
		}
	}
	return nil
}

// insideRoot checks that dir, or its deepest existing ancestor, resolves to a
// path inside realRoot: a directory of the repository may be a symbolic link.
func insideRoot(realRoot, dir string) error {
	for {
		real, err := filepath.EvalSymlinks(dir)
		if err == nil {
			if real != realRoot && !strings.HasPrefix(real, realRoot+string(filepath.Separator)) {
				return errors.New("a symbolic link in the repository points outside the stack's directory")
			}
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return err
		}
		dir = parent
	}
}

// compactJSON writes a resolved value as JSON on one line, which Terraform
// also reads as HCL in TF_VAR_ of complex types.
func compactJSON(v *stackconfig.Value) string {
	var buf, out bytes.Buffer
	writeJSON(&buf, v, "")
	if json.Compact(&out, buf.Bytes()) != nil {
		return buf.String()
	}
	return out.String()
}

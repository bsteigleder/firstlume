package stateid

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

const (
	// maxCodeFiles bounds how many files one identification reads from Git.
	maxCodeFiles = 800
	// maxModuleDepth bounds how far local modules are followed.
	maxModuleDepth = 8

	terragruntFile = "terragrunt.hcl"
	rootConfigFile = "root.hcl"
	cacheDir       = ".terragrunt-cache"
)

// ErrTooMuchCode says the stack has more files than one identification reads.
var ErrTooMuchCode = errors.New("the stack has too many files to read")

// Repo is a repository at one commit, read without cloning it.
type Repo struct {
	// Files are the paths of its files.
	Files []string
	Read  func(ctx context.Context, path string) ([]byte, error)
}

var (
	resourceBlock = regexp.MustCompile(`(?m)^[ \t]*resource[ \t]+"([^"]+)"[ \t]+"([^"]+)"`)
	moduleBlock   = regexp.MustCompile(`(?m)^[ \t]*module[ \t]+"([^"]+)"`)
	terraformHead = regexp.MustCompile(`(?m)^[ \t]*terraform[ \t]*\{`)
	sourceLine    = regexp.MustCompile(`(?m)^[ \t]*source[ \t]*=[ \t]*"(.*)"[ \t]*(?:[#/].*)?$`)
	nextBlock     = regexp.MustCompile(`(?m)^[a-z_]+[ \t]+("|\{)`)
	literal       = regexp.MustCompile(`"([^"\n$]{3,80})"`)
	parentFolder  = regexp.MustCompile(`^\$\{find_in_parent_folders\("([^"]+)"\)\}(.*)$`)
	repoRootFunc  = regexp.MustCompile(`^\$\{(?:get_repo_root\(\)|get_path_to_repo_root\(\))\}/?(.*)$`)
	unitDirFunc   = regexp.MustCompile(`^\$\{get_terragrunt_dir\(\)\}/?(.*)$`)
)

// ReadUnits lists the units of the stack at stackPath and what their code
// declares. A plain Terraform stack is one root unit. With Terragrunt, every
// folder with a terragrunt.hcl is a unit (only the stack's own folder without
// run --all), keyed relative to the nearest root.hcl above it, which is what
// path_relative_to_include() gives, or else to the stack.
func ReadUnits(ctx context.Context, repo Repo, stackPath string, terragrunt, runAll bool) ([]Unit, error) {
	stackPath = path.Clean(stackPath)
	r := &reader{ctx: ctx, repo: repo, cache: map[string]string{}, dirs: map[string]bool{}, hasFile: map[string]bool{}, byDir: map[string][]string{}}
	for _, f := range repo.Files {
		if strings.Contains("/"+f+"/", "/"+cacheDir+"/") {
			continue
		}
		r.hasFile[f] = true
		dir := path.Dir(f)
		r.byDir[dir] = append(r.byDir[dir], f)
		for d := dir; d != "." && !r.dirs[d]; d = path.Dir(d) {
			r.dirs[d] = true
		}
	}

	var dirs []string
	if terragrunt {
		for d := range r.byDir {
			if !r.hasFile[path.Join(d, terragruntFile)] || !within(d, stackPath) {
				continue
			}
			if runAll || d == stackPath {
				dirs = append(dirs, d)
			}
		}
		sort.Strings(dirs)
		// A terragrunt.hcl at the stack's folder with units below it is their
		// shared configuration, not a unit.
		if len(dirs) > 1 && dirs[0] == stackPath {
			dirs = dirs[1:]
		}
	}
	if len(dirs) == 0 {
		dirs = []string{stackPath}
		terragrunt = false
	}

	units := make([]Unit, 0, len(dirs))
	for _, d := range dirs {
		u := Unit{Addresses: map[string]bool{}, Literals: map[string]bool{}}
		if terragrunt {
			u.Key = r.unitKey(d, stackPath)
		}
		if err := r.readUnit(&u, d, terragrunt); err != nil {
			return nil, err
		}
		units = append(units, u)
	}
	return units, nil
}

type reader struct {
	ctx     context.Context
	repo    Repo
	cache   map[string]string
	dirs    map[string]bool
	hasFile map[string]bool
	byDir   map[string][]string
}

func within(dir, parent string) bool {
	return parent == "." || dir == parent || strings.HasPrefix(dir, parent+"/")
}

func (r *reader) unitKey(dir, stackPath string) string {
	base := stackPath
	for d := path.Dir(dir); ; d = path.Dir(d) {
		if r.hasFile[path.Join(d, rootConfigFile)] {
			base = d
			break
		}
		if d == "." || d == "/" {
			break
		}
	}
	if base == "." {
		return dir
	}
	if dir == base {
		return ""
	}
	return strings.TrimPrefix(dir, base+"/")
}

func (r *reader) read(file string) (string, error) {
	if c, ok := r.cache[file]; ok {
		return c, nil
	}
	if len(r.cache) >= maxCodeFiles {
		return "", ErrTooMuchCode
	}
	data, err := r.repo.Read(r.ctx, file)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", file, err)
	}
	c := stripComments(string(data))
	r.cache[file] = c
	return c, nil
}

// readUnit reads the unit's own files for values and follows its code.
func (r *reader) readUnit(u *Unit, dir string, terragrunt bool) error {
	for _, f := range r.byDir[dir] {
		if !strings.HasSuffix(f, ".tf") && !strings.HasSuffix(f, ".tfvars") && path.Base(f) != terragruntFile {
			continue
		}
		c, err := r.read(f)
		if err != nil {
			return err
		}
		for _, m := range literal.FindAllStringSubmatch(c, -1) {
			u.Literals[strings.ToLower(m[1])] = true
		}
	}
	if !terragrunt {
		return r.walk(u, dir, "", 0)
	}
	c, err := r.read(path.Join(dir, terragruntFile))
	if err != nil {
		return err
	}
	src := terraformSource(c)
	if src == "" {
		return r.walk(u, dir, "", 0)
	}
	target, ok := r.resolve(src, dir)
	if !ok {
		u.Opaque = append(u.Opaque, "")
		return nil
	}
	return r.walk(u, target, "", 0)
}

// walk adds the resources declared in dir, and in the local modules it calls,
// under prefix. A module it can't read is marked opaque, so its resources
// neither count for nor against this folder (R9.10).
func (r *reader) walk(u *Unit, dir, prefix string, depth int) error {
	if depth > maxModuleDepth {
		u.Opaque = append(u.Opaque, prefix)
		return nil
	}
	for _, f := range r.byDir[dir] {
		if !strings.HasSuffix(f, ".tf") {
			continue
		}
		c, err := r.read(f)
		if err != nil {
			return err
		}
		for _, m := range resourceBlock.FindAllStringSubmatch(c, -1) {
			u.Addresses[prefix+m[1]+"."+m[2]] = true
		}
		for _, m := range moduleBlock.FindAllStringSubmatchIndex(c, -1) {
			name := c[m[2]:m[3]]
			inner := prefix + "module." + name + "."
			target, ok := r.resolve(sourceAfter(c[m[1]:]), dir)
			if !ok {
				u.Opaque = append(u.Opaque, inner)
				continue
			}
			if err := r.walk(u, target, inner, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

// terraformSource is the source of a terragrunt.hcl's terraform block.
func terraformSource(c string) string {
	loc := terraformHead.FindStringIndex(c)
	if loc == nil {
		return ""
	}
	return sourceAfter(c[loc[1]:])
}

// sourceAfter is the first source attribute in the block that starts here.
func sourceAfter(rest string) string {
	if next := nextBlock.FindStringIndex(rest); next != nil {
		rest = rest[:next[0]]
	}
	if m := sourceLine.FindStringSubmatch(rest); m != nil {
		return m[1]
	}
	return ""
}

// resolve turns a local source into a folder of the repository. ok is false
// for a registry or Git source, and for one it can't work out.
func (r *reader) resolve(src, dir string) (string, bool) {
	src, _, _ = strings.Cut(src, "?")
	var target string
	switch {
	case src == "":
		return "", false
	case strings.HasPrefix(src, "./"), strings.HasPrefix(src, "../"):
		target = path.Join(dir, strings.Replace(src, "//", "/", 1))
	case repoRootFunc.MatchString(src):
		target = path.Clean(strings.Replace(repoRootFunc.FindStringSubmatch(src)[1], "//", "/", 1))
	case unitDirFunc.MatchString(src):
		target = path.Join(dir, strings.Replace(unitDirFunc.FindStringSubmatch(src)[1], "//", "/", 1))
	case parentFolder.MatchString(src):
		m := parentFolder.FindStringSubmatch(src)
		found := ""
		for d := path.Dir(dir); ; d = path.Dir(d) {
			if c := path.Join(d, m[1]); r.dirs[c] {
				found = c
				break
			}
			if d == "." || d == "/" {
				break
			}
		}
		if found == "" {
			return "", false
		}
		target = path.Join(found, strings.Replace(strings.TrimPrefix(m[2], "/"), "//", "/", 1))
	default:
		return "", false
	}
	if target == ".." || strings.HasPrefix(target, "../") || !r.dirs[target] {
		return "", false
	}
	return target, true
}

// stripComments removes whole-line comments, so a commented-out resource
// doesn't count. Comments after code stay: nothing here reads those.
func stripComments(c string) string {
	lines := strings.Split(c, "\n")
	for i, l := range lines {
		t := strings.TrimLeft(l, " \t")
		if strings.HasPrefix(t, "#") || strings.HasPrefix(t, "//") {
			lines[i] = ""
		}
	}
	return strings.Join(lines, "\n")
}

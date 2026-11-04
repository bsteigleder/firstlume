package stackconfig

import (
	"fmt"
	"path"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/bsteigleder/firstlume/internal/api"
)

// parseTriggers reads which files count for a stack (spec 0006, R4.2).
func parseTriggers(n *yaml.Node) (*api.StackTriggers, error) {
	if n.Kind != yaml.MappingNode {
		return nil, lineErr(n, "triggers must be a mapping with paths and ignore")
	}
	var t api.StackTriggers
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		var list *[]string
		switch k.Value {
		case "paths":
			list = &t.Paths
		case "ignore":
			list = &t.Ignore
		default:
			return nil, lineErr(k, "unknown key %q in triggers", k.Value)
		}
		if err := v.Decode(list); err != nil {
			return nil, lineErr(v, "triggers.%s must be a list of paths", k.Value)
		}
		for _, p := range *list {
			if err := validateTriggerGlob(p); err != nil {
				return nil, lineErr(v, "triggers.%s: %v", k.Value, err)
			}
		}
	}
	return &t, nil
}

func validateTriggerGlob(p string) error {
	if p == "" || strings.HasPrefix(p, "/") {
		return fmt.Errorf("%q must be relative to the repository's root", p)
	}
	for _, s := range strings.Split(p, "/") {
		if s == ".." {
			return fmt.Errorf("%q must stay inside the repository", p)
		}
		if s == "**" {
			continue
		}
		if strings.Contains(s, "**") {
			return fmt.Errorf("%q: ** must be a whole path segment", p)
		}
		if _, err := path.Match(s, ""); err != nil {
			return fmt.Errorf("%q is not a valid pattern", p)
		}
	}
	return nil
}

// MatchGlob tells whether a file matches a pattern where ** is any number of
// directories and the rest works like path.Match, one directory level at a time.
func MatchGlob(pattern, file string) bool {
	return matchSegments(strings.Split(pattern, "/"), strings.Split(file, "/"))
}

func matchSegments(pattern, file []string) bool {
	if len(pattern) == 0 {
		return len(file) == 0
	}
	if pattern[0] == "**" {
		for i := 0; i <= len(file); i++ {
			if matchSegments(pattern[1:], file[i:]) {
				return true
			}
		}
		return false
	}
	if len(file) == 0 {
		return false
	}
	ok, err := path.Match(pattern[0], file[0])
	return err == nil && ok && matchSegments(pattern[1:], file[1:])
}

func matchesAny(patterns []string, file string) bool {
	for _, p := range patterns {
		if MatchGlob(p, file) {
			return true
		}
	}
	return false
}

// Affected tells whether a change to these files, paths relative to the
// repository's root, concerns the stack in stackPath (spec 0006, R4.1, R4.2).
func Affected(stackPath string, t *api.StackTriggers, files []string) bool {
	if t == nil {
		t = &api.StackTriggers{}
	}
	stackPath = strings.Trim(stackPath, "/")
	for _, f := range files {
		switch {
		case matchesAny(t.Ignore, f):
		case stackPath == "" || stackPath == "." || f == stackPath || strings.HasPrefix(f, stackPath+"/"):
			return true
		case matchesAny(t.Paths, f):
			return true
		}
	}
	return false
}

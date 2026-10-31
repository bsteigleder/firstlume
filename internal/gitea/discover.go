package gitea

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"

	"github.com/bsteigleder/firstlume/internal/api"
	"github.com/bsteigleder/firstlume/internal/stackconfig"
)

// maxStacks bounds how many .firstlume folders one discovery reads configs for.
const maxStacks = 200

// Found is a .firstlume folder in a repository: the stack directory holding
// it, and its configuration or why that is invalid.
type Found struct {
	Path   string
	Config *api.StackConfig
	Error  string
}

// Discover lists the .firstlume folders in a repository at a commit, without cloning it.
func (c *Client) Discover(ctx context.Context, owner, name, sha string) ([]Found, error) {
	entries, err := c.Tree(ctx, owner, name, sha)
	if err != nil {
		return nil, err
	}
	dirs := map[string]bool{}
	configs := map[string]bool{}
	for _, e := range entries {
		switch {
		case e.Type == "tree" && path.Base(e.Path) == stackconfig.Dir:
			dirs[path.Dir(e.Path)] = true
		case e.Type == "blob" && path.Base(e.Path) == stackconfig.File && path.Base(path.Dir(e.Path)) == stackconfig.Dir:
			configs[path.Dir(path.Dir(e.Path))] = true
		}
	}
	paths := make([]string, 0, len(dirs))
	for d := range dirs {
		paths = append(paths, d)
	}
	sort.Strings(paths)
	if len(paths) > maxStacks {
		return nil, fmt.Errorf("found %d .firstlume folders; at most %d are supported per repository", len(paths), maxStacks)
	}

	out := make([]Found, 0, len(paths))
	for _, p := range paths {
		f := Found{Path: p}
		file := path.Join(p, stackconfig.Dir, stackconfig.File)
		if !configs[p] {
			f.Error = file + " not found"
			out = append(out, f)
			continue
		}
		data, err := c.File(ctx, owner, name, sha, file)
		if err != nil {
			if errors.Is(err, ErrUnauthorized) {
				return nil, err
			}
			f.Error = fmt.Sprintf("%s: %v", file, err)
		} else if f.Config, err = stackconfig.Parse(data); err != nil {
			f.Error = fmt.Sprintf("%s: %v", file, err)
		}
		out = append(out, f)
	}
	return out, nil
}

// Package stackconfig reads .firstlume/config.yaml, the file that marks a
// directory of a repository as a stack and pins the tooling it runs with.
package stackconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"sigs.k8s.io/yaml"

	"github.com/bsteigleder/firstlume/internal/api"
)

const (
	// Dir marks a stack's root directory inside a repository.
	Dir = ".firstlume"
	// File is the stack configuration inside Dir.
	File = "config.yaml"
)

// RelPath is the configuration file relative to a stack's root directory.
var RelPath = filepath.Join(Dir, File)

var versionRe = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$`)

// Parse validates a config.yaml. Unknown fields are errors, so typos surface
// instead of being silently ignored.
func Parse(data []byte) (*api.StackConfig, error) {
	var c api.StackConfig
	if err := yaml.UnmarshalStrict(data, &c); err != nil {
		return nil, err
	}
	switch {
	case c.Version != 1:
		return nil, fmt.Errorf("version must be 1, got %d", c.Version)
	case c.Engine != "terraform" && c.Engine != "opentofu":
		return nil, fmt.Errorf("engine must be terraform or opentofu, got %q", c.Engine)
	case !versionRe.MatchString(c.EngineVersion):
		return nil, errors.New("engine_version must be an exact version like 1.9.8")
	case c.TerragruntVersion != "" && !versionRe.MatchString(c.TerragruntVersion):
		return nil, errors.New("terragrunt_version must be an exact version like 0.77.0")
	case c.RunAll && c.TerragruntVersion == "":
		return nil, errors.New("run_all needs terragrunt_version")
	}
	return &c, nil
}

// Load reads and validates the config of the stack rooted at dir. Errors name
// the file, relative to the repository, as given by displayPath.
func Load(dir, displayPath string) (*api.StackConfig, error) {
	shown := filepath.ToSlash(filepath.Join(displayPath, RelPath))
	data, err := os.ReadFile(filepath.Join(dir, RelPath))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%s not found: a stack's directory needs %s/%s", shown, Dir, File)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", shown, err)
	}
	c, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", shown, err)
	}
	return c, nil
}

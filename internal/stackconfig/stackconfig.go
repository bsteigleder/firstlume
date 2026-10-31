// Package stackconfig reads a stack's .firstlume/config.yaml: the tooling it
// runs with and, optionally, the declaration of what a run needs besides the
// code (spec 0003).
package stackconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"go.yaml.in/yaml/v3"

	"github.com/bsteigleder/firstlume/internal/api"
)

const (
	Dir  = ".firstlume"
	File = "config.yaml"
)

var RelPath = filepath.Join(Dir, File)

var versionRe = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$`)

// Parse reads a config file. The declaration's summary, without values, is
// in the returned config.
func Parse(data []byte) (*api.StackConfig, error) {
	c, _, err := ParseDeclaration(data)
	return c, err
}

// ParseDeclaration reads a config file and its declaration, values included.
// The declaration is nil when the file declares nothing.
func ParseDeclaration(data []byte) (*api.StackConfig, *Declaration, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, nil, errors.New("the file must be a YAML mapping")
	}
	var c api.StackConfig
	var d Declaration
	root := doc.Content[0]
	for i := 0; i+1 < len(root.Content); i += 2 {
		k, v := root.Content[i], root.Content[i+1]
		var err error
		switch k.Value {
		case "version":
			err = v.Decode(&c.Version)
		case "engine":
			c.Engine, err = scalarText(v)
		case "engine_version":
			c.EngineVersion, err = scalarText(v)
		case "terragrunt_version":
			c.TerragruntVersion, err = scalarText(v)
		case "run_all":
			err = v.Decode(&c.RunAll)
		case "files":
			d.Files, err = parseFiles(v)
		case "env":
			d.Env, err = parseVariables(v, "env")
		case "terraform":
			d.Terraform, err = parseVariables(v, "terraform")
		case "registries":
			d.Registries, err = parseRegistries(v)
		default:
			err = lineErr(k, "unknown key %q", k.Value)
		}
		if err != nil {
			return nil, nil, err
		}
	}
	switch {
	case c.Version != 1:
		return nil, nil, fmt.Errorf("version must be 1, got %d", c.Version)
	case c.Engine != "terraform" && c.Engine != "opentofu":
		return nil, nil, fmt.Errorf("engine must be terraform or opentofu, got %q", c.Engine)
	case !versionRe.MatchString(c.EngineVersion):
		return nil, nil, errors.New("engine_version must be an exact version like 1.9.8")
	case c.TerragruntVersion != "" && !versionRe.MatchString(c.TerragruntVersion):
		return nil, nil, errors.New("terragrunt_version must be an exact version like 0.77.0")
	case c.RunAll && c.TerragruntVersion == "":
		return nil, nil, errors.New("run_all needs terragrunt_version")
	}
	if d.empty() {
		return &c, nil, nil
	}
	c.Declaration = d.Summary()
	return &c, &d, nil
}

// Load reads the config file of the stack in dir; displayPath names the
// stack's directory in errors.
func Load(dir, displayPath string) (*api.StackConfig, error) {
	c, _, err := LoadDeclaration(dir, displayPath)
	return c, err
}

// LoadDeclaration is Load with the declaration's values.
func LoadDeclaration(dir, displayPath string) (*api.StackConfig, *Declaration, error) {
	shown := filepath.ToSlash(filepath.Join(displayPath, RelPath))
	data, err := os.ReadFile(filepath.Join(dir, RelPath))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, fmt.Errorf("%s not found: a stack's directory needs %s/%s", shown, Dir, File)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", shown, err)
	}
	c, d, err := ParseDeclaration(data)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", shown, err)
	}
	return c, d, nil
}

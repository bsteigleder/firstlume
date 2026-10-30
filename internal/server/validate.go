package server

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/bsteigleder/firstlume/internal/api"
)

var (
	uuidRe    = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	nameRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]{0,99}$`)
	versionRe = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$`)
	// Git refs are passed to git commands, so they must never look like an option.
	refRe        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,254}$`)
	envKeyRe     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,254}$`)
	tfVarKeyRe   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,254}$`)
	scpLikeGitRe = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[A-Za-z0-9._/~-]+$`)
)

func validUUID(s string) bool { return uuidRe.MatchString(s) }

func validateName(field, s string) error {
	if !nameRe.MatchString(s) {
		return fmt.Errorf("%s must be 1-100 characters of letters, digits, space, '.', '_' or '-'", field)
	}
	return nil
}

func validateRef(s string) error {
	if !refRe.MatchString(s) || strings.Contains(s, "..") || strings.HasSuffix(s, "/") || strings.HasSuffix(s, ".lock") {
		return fmt.Errorf("invalid git ref %q", s)
	}
	return nil
}

func validateRepository(s string) error {
	switch {
	case strings.HasPrefix(s, "https://"), strings.HasPrefix(s, "ssh://"), strings.HasPrefix(s, "file://"):
		if strings.ContainsAny(s, " \t\n") {
			return fmt.Errorf("repository URL must not contain whitespace")
		}
		return nil
	case scpLikeGitRe.MatchString(s):
		return nil
	}
	return fmt.Errorf("repository must be an https://, ssh://, file:// or user@host:path git URL")
}

func validateTemplatePath(p string) error {
	if p == "" {
		return nil
	}
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, "-") {
		return fmt.Errorf("path must be relative to the repository root")
	}
	if clean := path.Clean(p); clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("path must stay inside the repository")
	}
	return nil
}

func validateTemplate(req *api.CreateTemplateRequest) error {
	if err := validateName("name", req.Name); err != nil {
		return err
	}
	if err := validateRepository(req.Repository); err != nil {
		return err
	}
	if err := validateTemplatePath(req.Path); err != nil {
		return err
	}
	if req.DefaultRef == "" {
		req.DefaultRef = "main"
	}
	if err := validateRef(req.DefaultRef); err != nil {
		return err
	}
	if req.Engine != "terraform" && req.Engine != "opentofu" {
		return fmt.Errorf("engine must be terraform or opentofu")
	}
	if !versionRe.MatchString(req.EngineVersion) {
		return fmt.Errorf("engine_version must be an exact version like 1.9.8")
	}
	if req.TerragruntVersion != "" && !versionRe.MatchString(req.TerragruntVersion) {
		return fmt.Errorf("terragrunt_version must be an exact version like 0.77.0")
	}
	return nil
}

func validateVariables(vars []api.Variable) error {
	seen := map[string]bool{}
	for _, v := range vars {
		id := string(v.Category) + "/" + v.Key
		if seen[id] {
			return fmt.Errorf("duplicate variable %s", id)
		}
		seen[id] = true
		switch v.Category {
		case api.CategoryEnv:
			if !envKeyRe.MatchString(v.Key) {
				return fmt.Errorf("invalid environment variable name %q", v.Key)
			}
			if reservedEnv(v.Key) {
				return fmt.Errorf("environment variable %q is reserved", v.Key)
			}
		case api.CategoryTerraform:
			if !tfVarKeyRe.MatchString(v.Key) {
				return fmt.Errorf("invalid terraform variable name %q", v.Key)
			}
		default:
			return fmt.Errorf("variable category must be env or terraform")
		}
	}
	return nil
}

func reservedEnv(key string) bool {
	upper := strings.ToUpper(key)
	switch upper {
	case "PATH", "HOME", "TF_IN_AUTOMATION", "TF_INPUT", "TF_CLI_CONFIG_FILE", "TF_DATA_DIR", "TF_PLUGIN_CACHE_DIR":
		return true
	}
	return strings.HasPrefix(upper, "FIRSTLUME_") || strings.HasPrefix(upper, "GIT_")
}

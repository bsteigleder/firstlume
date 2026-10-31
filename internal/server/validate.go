package server

import (
	"fmt"
	"path"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/bsteigleder/firstlume/internal/api"
)

var (
	uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]{0,99}$`)
	slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*[a-z0-9]$`)
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

// cleanName trims a user-chosen name (organization, folder, stack): 1-100
// characters, not only spaces, no '/' and no control characters.
func cleanName(field, s string) (string, error) {
	s = strings.TrimSpace(s)
	n := utf8.RuneCountInString(s)
	if n == 0 || n > 100 || !utf8.ValidString(s) {
		return "", fmt.Errorf("%s must be 1-100 characters", field)
	}
	for _, r := range s {
		if r == '/' || unicode.IsControl(r) {
			return "", fmt.Errorf("%s must not contain '/' or control characters", field)
		}
	}
	return s, nil
}

func validateSlug(s string) error {
	if !slugRe.MatchString(s) || len(s) < 2 || len(s) > 40 {
		return fmt.Errorf("slug must be 2-40 lowercase letters, digits or '-', starting and ending with a letter or digit")
	}
	return nil
}

// cleanStackPath normalizes the directory holding a stack's .firstlume
// folder, relative to the repository root ("." for the root).
func cleanStackPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if strings.HasPrefix(p, "/") || strings.Contains(p, `\`) {
		return "", fmt.Errorf("path must be relative to the repository root")
	}
	clean := path.Clean(p) // "" becomes "."
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("path must stay inside the repository")
	}
	if strings.HasPrefix(clean, "-") {
		return "", fmt.Errorf("path must not start with '-'")
	}
	return clean, nil
}

func cleanRepository(s string) (string, error) {
	s = strings.TrimRight(strings.TrimSpace(s), "/")
	return s, validateRepository(s)
}

func normalizeStack(req *api.CreateStackRequest) error {
	var err error
	if req.Name, err = cleanName("name", req.Name); err != nil {
		return err
	}
	if req.FolderID != "" && !validUUID(req.FolderID) {
		return fmt.Errorf("folder_id must be a folder id")
	}
	// Clone URLs of connection repositories come from the Git provider itself.
	if req.ConnectionID == "" {
		if req.Repository, err = cleanRepository(req.Repository); err != nil {
			return err
		}
	}
	if req.Ref = strings.TrimSpace(req.Ref); req.Ref == "" {
		req.Ref = "main"
	}
	if err := validateRef(req.Ref); err != nil {
		return err
	}
	req.Path, err = cleanStackPath(req.Path)
	return err
}

func normalizeStackUpdate(req *api.UpdateStackRequest) error {
	if req.Name != nil {
		name, err := cleanName("name", *req.Name)
		if err != nil {
			return err
		}
		req.Name = &name
	}
	if req.Repository != nil {
		repo, err := cleanRepository(*req.Repository)
		if err != nil {
			return err
		}
		req.Repository = &repo
	}
	if req.Ref != nil {
		ref := strings.TrimSpace(*req.Ref)
		if err := validateRef(ref); err != nil {
			return err
		}
		req.Ref = &ref
	}
	if req.Path != nil {
		p, err := cleanStackPath(*req.Path)
		if err != nil {
			return err
		}
		req.Path = &p
	}
	return nil
}

func validateLevelSettings(s *api.LevelSettings) error {
	if m := s.DriftIntervalMinutes; m != nil && *m != 0 && (*m < api.MinDriftIntervalMinutes || *m > api.MaxDriftIntervalMinutes) {
		return fmt.Errorf("drift_interval_minutes must be 0 (off) or between 15 and 10080")
	}
	if s.Variables == nil {
		s.Variables = []api.Variable{}
	}
	return validateVariables(s.Variables)
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

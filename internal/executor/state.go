package executor

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bsteigleder/firstlume/internal/api"
)

// stateEnv points Terraform's http backend at the stack's state in Firstlume
// (spec 0002, R1.2). Code that declares `backend "http" {}` gets the root
// unit; Terragrunt builds each unit's address from FIRSTLUME_STATE_URL. Code
// with another backend ignores all of it.
func stateEnv(apiURL string, s *api.ExecSpec) []string {
	if s.StateToken == "" {
		return nil
	}
	base := strings.TrimRight(apiURL, "/") + "/state/v1/stacks/" + s.StackID
	return []string{
		"TF_HTTP_ADDRESS=" + base,
		"TF_HTTP_LOCK_ADDRESS=" + base,
		"TF_HTTP_UNLOCK_ADDRESS=" + base,
		"TF_HTTP_USERNAME=firstlume",
		"TF_HTTP_PASSWORD=" + s.StateToken,
		"FIRSTLUME_STATE_URL=" + base,
	}
}

const backendHelp = `Declare where the state lives. For Firstlume's state backend:
  Terraform:  terraform { backend "http" {} }
  Terragrunt (root.hcl):
    remote_state {
      backend  = "http"
      generate = { path = "backend.tf", if_exists = "overwrite_terragrunt" }
      config = {
        address        = "${get_env("FIRSTLUME_STATE_URL")}/${path_relative_to_include()}"
        lock_address   = "${get_env("FIRSTLUME_STATE_URL")}/${path_relative_to_include()}"
        unlock_address = "${get_env("FIRSTLUME_STATE_URL")}/${path_relative_to_include()}"
      }
    }`

// checkBackends fails when a unit keeps its state locally, since the runner's
// disk is gone when the run ends (R2.2). With run --all (plansJSON set), the
// units are the ones Terragrunt planned: they have a JSON plan there.
func checkBackends(workDir, plansJSON string, out io.Writer) error {
	runAll := plansJSON != ""
	units := []string{"."}
	if runAll {
		var err error
		if units, err = plannedUnits(plansJSON); err != nil {
			return err
		}
	}
	var local []string
	types := map[string][]string{}
	for _, u := range units {
		typ, err := unitBackend(filepath.Join(workDir, filepath.FromSlash(u)))
		if err != nil {
			return fmt.Errorf("reading the backend of %s: %w", u, err)
		}
		if typ == "" || typ == "local" {
			local = append(local, u)
			continue
		}
		types[typ] = append(types[typ], u)
	}
	if len(local) > 0 {
		what := "This stack keeps its state"
		if runAll {
			what = "These units keep their state"
		}
		if runAll || local[0] != "." {
			what += " (" + strings.Join(local, ", ") + ")"
		}
		fmt.Fprintf(out, "==> %s on the runner's disk, which is gone when the run ends.\n%s\n", what, backendHelp)
		return errors.New("no remote state backend: the state would be lost")
	}
	names := make([]string, 0, len(types))
	for t := range types {
		names = append(names, t)
	}
	sort.Strings(names)
	for _, t := range names {
		label := t
		if t == "http" {
			label = "http (Firstlume, unless the code points elsewhere)"
		}
		if runAll {
			fmt.Fprintf(out, "==> State backend %s: %d unit(s)\n", label, len(types[t]))
		} else {
			fmt.Fprintf(out, "==> State backend: %s\n", label)
		}
	}
	return nil
}

// plannedUnits lists the units with a JSON plan under dir, as slash paths.
func plannedUnits(dir string) ([]string, error) {
	var units []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "tfplan.json" {
			return err
		}
		rel, err := filepath.Rel(dir, filepath.Dir(path))
		if err != nil {
			return err
		}
		units = append(units, filepath.ToSlash(rel))
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	sort.Strings(units)
	return units, err
}

// unitBackend returns the backend type `init` configured for a unit, or ""
// when it has none (local state). Terragrunt units with a `source` are
// initialized inside their .terragrunt-cache.
func unitBackend(dir string) (string, error) {
	candidates := []string{filepath.Join(dir, ".terraform", "terraform.tfstate")}
	cache := filepath.Join(dir, ".terragrunt-cache")
	_ = filepath.WalkDir(cache, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() == "terraform.tfstate" && filepath.Base(filepath.Dir(path)) == ".terraform" {
			candidates = append(candidates, path)
		}
		return nil
	})
	for _, c := range candidates {
		raw, err := os.ReadFile(c)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		var st struct {
			Backend *struct {
				Type string `json:"type"`
			} `json:"backend"`
		}
		if err := json.Unmarshal(raw, &st); err != nil {
			return "", err
		}
		if st.Backend != nil {
			return st.Backend.Type, nil
		}
	}
	return "", nil
}

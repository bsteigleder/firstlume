package executor

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bsteigleder/firstlume/internal/api"
	"github.com/bsteigleder/firstlume/internal/stackconfig"
)

// Registry credentials (spec 0003, R4): written in the phase's own HOME as
// Docker's config.json, also used as Helm's registry config, and as
// TF_TOKEN_<host> for Terraform registries.

type registryLogin struct {
	host, kind, username, password, origin string
}

// registries resolves the credentials, the settings winning over the
// declaration for the same host, writes their files under home and returns
// the environment that points the tools at them.
func (r *resolver) registries(home string, fromSettings []api.ExecRegistry, declared []stackconfig.Registry, masker *Masker,
	out io.Writer) ([]string, error) {
	byHost := map[string]registryLogin{}
	for _, reg := range fromSettings {
		password := reg.Password
		if v := reg.PasswordVault; v != nil {
			var err error
			if password, err = r.vault.read(r.ctx, v.Path, v.Field); err != nil {
				return nil, fmt.Errorf("registry %s: vault %s#%s: %w", reg.Host, v.Path, v.Field, err)
			}
		}
		byHost[reg.Host] = registryLogin{reg.Host, reg.Kind, reg.Username, password, "settings"}
	}
	for _, reg := range declared {
		host := strings.ToLower(reg.Host)
		if _, ok := byHost[host]; ok {
			fmt.Fprintf(out, "==> registry %s is also set in the stack's settings, which win\n", host)
			continue
		}
		login := registryLogin{host: host, kind: reg.Kind, origin: fmt.Sprintf("%s line %d", stackconfig.File, reg.Line)}
		for _, f := range []struct {
			v   *stackconfig.Value
			dst *string
		}{{reg.Username, &login.username}, {reg.Password, &login.password}} {
			if f.v == nil {
				continue
			}
			res, err := r.resolve(f.v, dirVars{})
			if err != nil {
				return nil, fmt.Errorf("line %d: registry %s: %w", reg.Line, host, err)
			}
			*f.dst = scalarString(res)
		}
		byHost[host] = login
	}
	if len(byHost) == 0 {
		return nil, nil
	}

	hosts := make([]string, 0, len(byHost))
	for h := range byHost {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	auths := map[string]map[string]string{}
	var env []string
	for _, h := range hosts {
		login := byHost[h]
		masker.Add(login.password)
		fmt.Fprintf(out, "==> Registry credential for %s (%s, from %s)\n", h, login.kind, login.origin)
		if login.kind == "terraform" {
			env = append(env, terraformTokenVar(h)+"="+login.password)
			continue
		}
		auth := base64.StdEncoding.EncodeToString([]byte(login.username + ":" + login.password))
		masker.Add(auth)
		auths[h] = map[string]string{"auth": auth}
		if h == "docker.io" || h == "index.docker.io" {
			auths["https://index.docker.io/v1/"] = auths[h] // the key the Docker CLI looks up
		}
	}
	if len(auths) == 0 {
		return env, nil
	}
	data, err := json.MarshalIndent(map[string]any{"auths": auths}, "", "  ")
	if err != nil {
		return nil, err
	}
	dockerDir := filepath.Join(home, ".docker")
	helmConfig := filepath.Join(home, ".config", "helm", "registry", "config.json")
	for _, path := range []string{filepath.Join(dockerDir, "config.json"), helmConfig} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return nil, err
		}
	}
	return append(env, "DOCKER_CONFIG="+dockerDir, "HELM_REGISTRY_CONFIG="+helmConfig), nil
}

// terraformTokenVar is the variable Terraform and OpenTofu read a host's
// token from: dots become underscores and hyphens double underscores.
func terraformTokenVar(host string) string {
	return "TF_TOKEN_" + strings.NewReplacer(".", "_", "-", "__").Replace(strings.ToLower(host))
}

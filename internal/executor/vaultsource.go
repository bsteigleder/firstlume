package executor

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/bsteigleder/firstlume/internal/api"
	"github.com/bsteigleder/firstlume/internal/vault"
)

// vaultSource reads {vault: path, field: name} for one phase (spec 0003,
// R3.5): it logs in with the phase's JWT the first time it is needed, reads
// each path once, masks every value and keeps a keyed hash of each, which
// the apply compares with the plan's (R3.8).
type vaultSource struct {
	spec    *api.ExecVault
	masker  *Masker
	client  *vault.Client
	secrets map[string]map[string]any
	// hashes maps "path#field" to the keyed hash of the value read.
	hashes map[string]string
}

func newVaultSource(spec *api.ExecVault, masker *Masker) *vaultSource {
	if spec != nil {
		masker.Add(spec.JWT)
	}
	return &vaultSource{spec: spec, masker: masker, secrets: map[string]map[string]any{}, hashes: map[string]string{}}
}

func (v *vaultSource) login(ctx context.Context) error {
	switch {
	case v.client != nil:
		return nil
	case v.spec == nil:
		return errors.New("the organization has no Vault connection")
	case v.spec.Role == "":
		return errors.New("no Vault role is configured for this stack: set vault_role in its settings or a folder's")
	}
	c, err := vault.New(vault.Config{Address: v.spec.Address, CACert: v.spec.CACert, Namespace: v.spec.Namespace})
	if err != nil {
		return err
	}
	if err := c.Login(ctx, v.spec.AuthPath, v.spec.Role, v.spec.JWT); err != nil {
		return fmt.Errorf("logging in to Vault with role %s: %w", v.spec.Role, err)
	}
	v.masker.Add(c.Token())
	v.client = c
	return nil
}

func (v *vaultSource) read(ctx context.Context, path, field string) (string, error) {
	for _, seg := range strings.Split(path, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", fmt.Errorf("vault path %q is not valid once ${dir} and ${name} are replaced", path)
		}
	}
	data, ok := v.secrets[path]
	if !ok {
		if err := v.login(ctx); err != nil {
			return "", err
		}
		var err error
		if data, err = v.client.ReadSecret(ctx, path); err != nil {
			return "", fmt.Errorf("reading %s: %w", path, err)
		}
		v.secrets[path] = data
	}
	raw, ok := data[field]
	if !ok {
		return "", fmt.Errorf("the secret %s has no field %s", path, field)
	}
	value := vault.FieldString(raw)
	v.masker.Add(value)
	v.hashes[path+"#"+field] = v.hash(path+"#"+field, value)
	return value, nil
}

func (v *vaultSource) hash(ref, value string) string {
	key, _ := base64.RawURLEncoding.DecodeString(v.spec.HashKey)
	m := hmac.New(sha256.New, key)
	m.Write([]byte(ref + "\x00" + value))
	return hex.EncodeToString(m.Sum(nil))
}

// changedSince lists the values read now that differ from the plan's.
func (v *vaultSource) changedSince(plan map[string]string) []string {
	var changed []string
	for ref, h := range v.hashes {
		if plan[ref] != h {
			changed = append(changed, ref)
		}
	}
	sort.Strings(changed)
	return changed
}

// revoke ends the phase's Vault token, if it logged in.
func (v *vaultSource) revoke(ctx context.Context) error {
	if v.client == nil {
		return nil
	}
	return v.client.Revoke(ctx)
}

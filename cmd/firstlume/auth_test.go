package main

import (
	"strings"
	"testing"
)

// CA3.13: OIDC needs a public URL and a client; errors name what is missing
// and never carry the client secret.
func TestAuthConfig(t *testing.T) {
	const secret = "s3cr3t-value"
	base := map[string]string{
		"FIRSTLUME_PUBLIC_URL":          "https://firstlume.example.com/",
		"FIRSTLUME_OIDC_ISSUER":         "https://gitea.example.com/",
		"FIRSTLUME_OIDC_CLIENT_ID":      "firstlume",
		"FIRSTLUME_OIDC_CLIENT_SECRET":  secret,
		"FIRSTLUME_OIDC_ALLOWED_GROUPS": "acme, acme:infra",
	}
	set := func(t *testing.T, env map[string]string, drop string) {
		for _, k := range []string{"FIRSTLUME_PUBLIC_URL", "FIRSTLUME_OIDC_ISSUER", "FIRSTLUME_OIDC_CLIENT_ID",
			"FIRSTLUME_OIDC_CLIENT_SECRET", "FIRSTLUME_OIDC_ALLOWED_GROUPS", "FIRSTLUME_OIDC_SCOPES", "FIRSTLUME_ADMIN_TOKEN_LOGIN"} {
			t.Setenv(k, "")
		}
		for k, v := range env {
			if k != drop {
				t.Setenv(k, v)
			}
		}
	}

	t.Run("complete", func(t *testing.T) {
		set(t, base, "")
		cfg, err := authConfig()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.PublicURL != "https://firstlume.example.com" || cfg.OIDC == nil || !cfg.AdminTokenLogin {
			t.Fatalf("cfg = %+v", cfg)
		}
		o := cfg.OIDC
		if strings.Join(o.AllowedGroups, "|") != "acme|acme:infra" || strings.Join(o.Scopes, " ") != "openid profile email groups" ||
			o.GroupsClaim != "groups" || o.ProviderName != "Gitea" {
			t.Fatalf("oidc = %+v", o)
		}
	})
	for _, missing := range []string{"FIRSTLUME_PUBLIC_URL", "FIRSTLUME_OIDC_CLIENT_ID", "FIRSTLUME_OIDC_CLIENT_SECRET"} {
		t.Run("without "+missing, func(t *testing.T) {
			set(t, base, missing)
			_, err := authConfig()
			if err == nil || !strings.Contains(err.Error(), missing) {
				t.Fatalf("err = %v, want it to name %s", err, missing)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatal("the error carries the client secret")
			}
		})
	}
	t.Run("bad public URL", func(t *testing.T) {
		set(t, map[string]string{"FIRSTLUME_PUBLIC_URL": "firstlume.example.com"}, "")
		if _, err := authConfig(); err == nil || !strings.Contains(err.Error(), "FIRSTLUME_PUBLIC_URL") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("no OIDC", func(t *testing.T) {
		set(t, map[string]string{"FIRSTLUME_ADMIN_TOKEN_LOGIN": "false"}, "")
		cfg, err := authConfig()
		if err != nil || cfg.OIDC != nil || cfg.AdminTokenLogin {
			t.Fatalf("cfg = %+v, err = %v", cfg, err)
		}
	})
}

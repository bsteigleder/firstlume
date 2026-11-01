package api

import "time"

// User is a person who signed in through the OIDC provider (spec 0001, R7.2).
type User struct {
	ID        string   `json:"id"`
	Login     string   `json:"login"`
	Name      string   `json:"name"`
	Email     string   `json:"email"`
	AvatarURL string   `json:"avatar_url"`
	Groups    []string `json:"groups"`
	// Issuer and Subject identify the user at the provider.
	Issuer      string    `json:"-"`
	Subject     string    `json:"-"`
	LastLoginAt time.Time `json:"last_login_at"`
}

// Me is GET /api/v1/me: who is calling, and the organizations they belong to.
type Me struct {
	// Kind is "user" for a session, "admin-token" for the installation token.
	Kind string `json:"kind"`
	User *User  `json:"user,omitempty"`
	// Organizations the user is a member of. Until Marco 3b every signed-in
	// user can still open all of them (D8); this list drives the first-access
	// screen (R7.7). Empty for the admin token.
	Organizations []Organization `json:"organizations"`
}

// AuthConfig is GET /auth/config: what the login screen offers (R7.14).
type AuthConfig struct {
	OIDC            *AuthProvider `json:"oidc,omitempty"`
	AdminTokenLogin bool          `json:"admin_token_login"`
}

type AuthProvider struct {
	Name string `json:"name"`
	// Available is false while the provider's discovery document can't be
	// read; the button stays off and the server retries (R7.12).
	Available bool `json:"available"`
}

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
	// InstallationAdmin sees every organization and can create them: the admin
	// token, or a user in one of the admin groups (R7.15).
	InstallationAdmin bool `json:"installation_admin"`
	// Organizations the user is a member of. Empty for the admin token.
	Organizations []Organization `json:"organizations"`
}

// Member is someone in an organization (R7.16).
type Member struct {
	UserID    string    `json:"user_id"`
	Login     string    `json:"login"`
	Name      string    `json:"name"`
	AvatarURL string    `json:"avatar_url"`
	Since     time.Time `json:"since"`
}

// Invite is a pending invitation for someone who never signed in (R7.18).
type Invite struct {
	ID        string    `json:"id"`
	Login     string    `json:"login"`
	InvitedBy string    `json:"invited_by"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// MemberList is GET /orgs/{org}/members.
type MemberList struct {
	Members []Member `json:"members"`
	Invites []Invite `json:"invites"`
}

// InviteRequest is POST /orgs/{org}/members: the login at the provider.
type InviteRequest struct {
	Login string `json:"login"`
}

// InviteResult holds Member when the person has access now, or Invite when
// the invitation waits for their first sign-in.
type InviteResult struct {
	Member *Member `json:"member,omitempty"`
	Invite *Invite `json:"invite,omitempty"`
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

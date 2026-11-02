package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/bsteigleder/firstlume/internal/api"
	"github.com/bsteigleder/firstlume/internal/secrets"
)

// SessionTokenPrefix starts every session cookie value.
const SessionTokenPrefix = "flsess_"

const userColumns = `id, issuer, subject, login, name, email, avatar_url, groups, last_login_at`

// prefixed qualifies a column list for a join: prefixed("u.", "id, name") is
// "u.id, u.name".
func prefixed(alias, columns string) string {
	parts := strings.Split(columns, ",")
	for i, p := range parts {
		parts[i] = alias + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}

func scanUser(row pgx.Row) (*api.User, error) {
	var u api.User
	if err := row.Scan(&u.ID, &u.Issuer, &u.Subject, &u.Login, &u.Name, &u.Email, &u.AvatarURL, &u.Groups, &u.LastLoginAt); err != nil {
		return nil, notFound(err)
	}
	return &u, nil
}

// UpsertUser creates the user on their first login and refreshes the profile
// and groups on every later one (R7.2).
func (s *Store) UpsertUser(ctx context.Context, u api.User) (*api.User, error) {
	if u.Groups == nil {
		u.Groups = []string{}
	}
	return scanUser(s.pool.QueryRow(ctx, `
		INSERT INTO users (issuer, subject, login, name, email, avatar_url, groups)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (issuer, subject) DO UPDATE SET
			login = EXCLUDED.login, name = EXCLUDED.name, email = EXCLUDED.email,
			avatar_url = EXCLUDED.avatar_url, groups = EXCLUDED.groups, last_login_at = now()
		RETURNING `+userColumns,
		u.Issuer, u.Subject, u.Login, u.Name, u.Email, u.AvatarURL, u.Groups))
}

// CreateSession opens a session for the user. The token is returned only
// here; only its hash is stored (R7.3).
func (s *Store) CreateSession(ctx context.Context, userID string, ttl time.Duration) (string, error) {
	token, err := secrets.RandomToken(SessionTokenPrefix)
	if err != nil {
		return "", err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO sessions (user_id, token_hash, expires_at) VALUES ($1, $2, now() + $3::interval)`,
		userID, secrets.HashToken(token), ttl.String())
	if err != nil {
		return "", classify(err)
	}
	return token, nil
}

// SessionUser returns the user of a live session: not expired, not revoked.
func (s *Store) SessionUser(ctx context.Context, token string) (*api.User, error) {
	return scanUser(s.pool.QueryRow(ctx, `
		SELECT `+prefixed("u.", userColumns)+` FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1 AND s.revoked_at IS NULL AND s.expires_at > now()`,
		secrets.HashToken(token)))
}

// RevokeSession ends a session on the server, so the cookie stops working
// even if someone kept a copy (R7.3).
func (s *Store) RevokeSession(ctx context.Context, token string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE sessions SET revoked_at = now() WHERE token_hash = $1 AND revoked_at IS NULL`,
		secrets.HashToken(token))
	return err
}

// LoginAttempt is the server side of a login in progress.
type LoginAttempt struct {
	CodeVerifier string
	Nonce        string
	ReturnTo     string
}

// CreateLoginAttempt stores a login in progress under the hash of its OAuth
// state.
func (s *Store) CreateLoginAttempt(ctx context.Context, state string, a LoginAttempt, ttl time.Duration) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO login_attempts (state_hash, code_verifier, nonce, return_to, expires_at)
		VALUES ($1, $2, $3, $4, now() + $5::interval)`,
		secrets.HashToken(state), a.CodeVerifier, a.Nonce, a.ReturnTo, ttl.String())
	return err
}

// ConsumeLoginAttempt returns the login in progress for state and deletes it,
// so a state works once (CA3.2). Unknown, used and expired states are
// ErrNotFound.
func (s *Store) ConsumeLoginAttempt(ctx context.Context, state string) (*LoginAttempt, error) {
	var a LoginAttempt
	var expired bool
	err := s.pool.QueryRow(ctx, `
		DELETE FROM login_attempts WHERE state_hash = $1
		RETURNING code_verifier, nonce, return_to, expires_at <= now()`,
		secrets.HashToken(state)).Scan(&a.CodeVerifier, &a.Nonce, &a.ReturnTo, &expired)
	if err != nil {
		return nil, notFound(err)
	}
	if expired {
		return nil, ErrNotFound
	}
	return &a, nil
}

// PurgeAuth deletes logins never finished, expired invitations and sessions
// that ended more than a day ago.
func (s *Store) PurgeAuth(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM login_attempts WHERE expires_at <= now()`); err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM organization_invites WHERE expires_at <= now()`); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `
		DELETE FROM sessions WHERE expires_at < now() - interval '1 day' OR revoked_at < now() - interval '1 day'`)
	return err
}

// CreateOrganizationBy creates an organization and makes userID its
// administrator (R7.7).
func (s *Store) CreateOrganizationBy(ctx context.Context, req api.CreateOrganizationRequest, userID string) (*api.Organization, error) {
	var o *api.Organization
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		o, err = scanOrganization(tx.QueryRow(ctx,
			`INSERT INTO organizations (slug, name) VALUES ($1, $2) RETURNING `+organizationColumns, req.Slug, req.Name))
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO organization_members (organization_id, user_id) VALUES ($1, $2)`, o.ID, userID)
		return err
	})
	if err != nil {
		return nil, classify(err)
	}
	return o, nil
}

// UserOrganizations returns the organizations the user is a member of.
func (s *Store) UserOrganizations(ctx context.Context, userID string) ([]api.Organization, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+prefixed("o.", organizationColumns)+` FROM organizations o
		JOIN organization_members m ON m.organization_id = o.id
		WHERE m.user_id = $1 ORDER BY lower(o.name), o.slug`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.Organization{}
	for rows.Next() {
		o, err := scanOrganization(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *o)
	}
	return out, rows.Err()
}

// IsMember reports whether the user belongs to the organization (R7.16).
func (s *Store) IsMember(ctx context.Context, orgID, userID string) (bool, error) {
	var member bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM organization_members WHERE organization_id = $1 AND user_id = $2)`,
		orgID, userID).Scan(&member)
	return member, err
}

// AcceptInvites turns the live invitations for login into memberships. Each
// one works once, and expired ones give nothing (R7.18).
func (s *Store) AcceptInvites(ctx context.Context, userID, login string) error {
	_, err := s.pool.Exec(ctx, `
		WITH taken AS (
			DELETE FROM organization_invites WHERE lower(login) = lower($2) AND expires_at > now()
			RETURNING organization_id
		)
		INSERT INTO organization_members (organization_id, user_id)
		SELECT organization_id, $1 FROM taken
		ON CONFLICT DO NOTHING`, userID, login)
	return err
}

// ListMembers returns the organization's members and live invitations.
func (s *Store) ListMembers(ctx context.Context, orgID string) (*api.MemberList, error) {
	out := &api.MemberList{Members: []api.Member{}, Invites: []api.Invite{}}
	rows, err := s.pool.Query(ctx, `
		SELECT u.id, u.login, u.name, u.avatar_url, m.created_at
		FROM organization_members m JOIN users u ON u.id = m.user_id
		WHERE m.organization_id = $1 ORDER BY m.created_at, lower(u.login)`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var m api.Member
		if err := rows.Scan(&m.UserID, &m.Login, &m.Name, &m.AvatarURL, &m.Since); err != nil {
			return nil, err
		}
		out.Members = append(out.Members, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = s.pool.Query(ctx, `
		SELECT `+inviteColumns+` FROM organization_invites
		WHERE organization_id = $1 AND expires_at > now() ORDER BY created_at, lower(login)`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		i, err := scanInvite(rows)
		if err != nil {
			return nil, err
		}
		out.Invites = append(out.Invites, *i)
	}
	return out, rows.Err()
}

const inviteColumns = `id, login, invited_by, created_at, expires_at`

func scanInvite(row pgx.Row) (*api.Invite, error) {
	var i api.Invite
	if err := row.Scan(&i.ID, &i.Login, &i.InvitedBy, &i.CreatedAt, &i.ExpiresAt); err != nil {
		return nil, notFound(err)
	}
	return &i, nil
}

// Invite gives login access to the organization: a known user becomes a member
// at once, anyone else gets a pending invitation, renewed if there is one
// already. ErrDuplicate means they are a member already (R7.18).
func (s *Store) Invite(ctx context.Context, orgID, login, invitedBy string, ttl time.Duration) (*api.InviteResult, error) {
	out := &api.InviteResult{}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var m api.Member
		// Providers can reuse a login across issuers: the latest sign-in wins.
		err := tx.QueryRow(ctx, `
			SELECT id, login, name, avatar_url FROM users WHERE lower(login) = lower($1)
			ORDER BY last_login_at DESC LIMIT 1`, login).Scan(&m.UserID, &m.Login, &m.Name, &m.AvatarURL)
		if errors.Is(err, pgx.ErrNoRows) {
			out.Invite, err = scanInvite(tx.QueryRow(ctx, `
				INSERT INTO organization_invites (organization_id, login, invited_by, expires_at)
				VALUES ($1, $2, $3, now() + $4::interval)
				ON CONFLICT (organization_id, lower(login)) DO UPDATE SET
					login = EXCLUDED.login, invited_by = EXCLUDED.invited_by, created_at = now(), expires_at = EXCLUDED.expires_at
				RETURNING `+inviteColumns, orgID, login, invitedBy, ttl.String()))
			return err
		}
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			INSERT INTO organization_members (organization_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, orgID, m.UserID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrDuplicate
		}
		if err := tx.QueryRow(ctx, `
			SELECT created_at FROM organization_members WHERE organization_id = $1 AND user_id = $2`,
			orgID, m.UserID).Scan(&m.Since); err != nil {
			return err
		}
		out.Member = &m
		return nil
	})
	if err != nil {
		return nil, classify(err)
	}
	return out, nil
}

// RemoveMember takes the user out of the organization. Access ends at their
// next request, even with a session open (R7.6).
func (s *Store) RemoveMember(ctx context.Context, orgID, userID string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM organization_members WHERE organization_id = $1 AND user_id = $2`, orgID, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// CancelInvite deletes a pending invitation.
func (s *Store) CancelInvite(ctx context.Context, orgID, inviteID string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM organization_invites WHERE organization_id = $1 AND id = $2`, orgID, inviteID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/bsteigleder/firstlume/internal/api"
)

// A Git event is kept with the decision taken for each stack (spec 0006, R10).
// Events are processed in the background, one at a time per branch.

const (
	gitEventMaxAttempts = 5
	// gitEventClaimTimeout gives an event back when the replica processing it died.
	gitEventClaimTimeout = 5 * time.Minute
	// maxEventFiles bounds how many considered files an event keeps per stack.
	maxEventFiles = 100
)

// WebhookTarget is what checking and accepting a webhook needs.
type WebhookTarget struct {
	ConnectionID   string
	OrganizationID string
	Account        string
	Secret         []byte
}

// GetWebhookTarget returns ErrNotFound for an unknown connection or one with no secret yet.
func (s *Store) GetWebhookTarget(ctx context.Context, connectionID string) (*WebhookTarget, error) {
	t := WebhookTarget{ConnectionID: connectionID}
	var enc []byte
	err := s.pool.QueryRow(ctx, `
		SELECT organization_id, account, webhook_secret_enc FROM git_connections
		WHERE id = $1 AND webhook_secret_enc IS NOT NULL`, connectionID).Scan(&t.OrganizationID, &t.Account, &enc)
	if err != nil {
		return nil, notFound(err)
	}
	if t.Secret, err = s.keys.Decrypt(enc); err != nil {
		return nil, err
	}
	return &t, nil
}

// SetWebhookSecret replaces the secret of the organization's connection and returns the connection's id.
func (s *Store) SetWebhookSecret(ctx context.Context, orgID, secret string) (string, error) {
	enc, err := s.keys.Encrypt([]byte(secret))
	if err != nil {
		return "", err
	}
	var id string
	err = s.pool.QueryRow(ctx, `
		UPDATE git_connections SET webhook_secret_enc = $2, updated_at = now()
		WHERE organization_id = $1 RETURNING id`, orgID, enc).Scan(&id)
	return id, notFound(err)
}

// NewGitEvent is a webhook delivery to keep.
type NewGitEvent struct {
	OrganizationID, ConnectionID, Delivery, Kind, Source string
	Repository, Ref, BeforeSHA, AfterSHA, Sender         string
	TotalCommits                                         int
}

// AddGitEvent keeps a delivery. A delivery already received is not kept again
// and returns false.
func (s *Store) AddGitEvent(ctx context.Context, e NewGitEvent) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO git_events (organization_id, connection_id, delivery, kind, source, repository, ref, before_sha, after_sha, sender, total_commits)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (connection_id, delivery) DO NOTHING`,
		e.OrganizationID, e.ConnectionID, e.Delivery, e.Kind, e.Source, e.Repository, e.Ref, e.BeforeSHA, e.AfterSHA, e.Sender, e.TotalCommits)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ClaimedGitEvent is an event to process, with what it takes to reach Gitea.
type ClaimedGitEvent struct {
	ID, OrganizationID, ConnectionID, Repository, Ref string
	BeforeSHA, AfterSHA, Sender                       string
	TotalCommits, Attempts                            int
	Connection                                        *GitConnection
}

// ClaimGitEvent takes the oldest event ready to process, or returns
// ErrNotFound. An event waits while an older one of its branch isn't finished,
// so a later push never runs before an earlier one. It is safe with several
// server replicas.
func (s *Store) ClaimGitEvent(ctx context.Context) (*ClaimedGitEvent, error) {
	var e ClaimedGitEvent
	err := s.pool.QueryRow(ctx, `
		UPDATE git_events SET status = 'processing', claimed_at = now(), attempts = attempts + 1
		WHERE id = (
			SELECT g.id FROM git_events g
			WHERE g.not_before <= now()
			  AND (g.status = 'pending' OR (g.status = 'processing' AND g.claimed_at < now() - make_interval(secs => $1)))
			  AND NOT EXISTS (
				SELECT 1 FROM git_events o
				WHERE o.connection_id = g.connection_id AND o.repository = g.repository AND o.ref = g.ref
				  AND o.seq < g.seq AND o.status IN ('pending', 'processing'))
			ORDER BY g.seq
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING id, organization_id, connection_id, repository, ref, before_sha, after_sha, sender, total_commits, attempts`,
		gitEventClaimTimeout.Seconds()).Scan(&e.ID, &e.OrganizationID, &e.ConnectionID, &e.Repository, &e.Ref,
		&e.BeforeSHA, &e.AfterSHA, &e.Sender, &e.TotalCommits, &e.Attempts)
	if err != nil {
		return nil, notFound(err)
	}
	if e.Connection, err = s.GetGitConnection(ctx, e.OrganizationID); err != nil {
		return &e, err // retried like any other failure
	}
	return &e, nil
}

// FinishGitEvent records the decisions. Processing the event again replaces them.
func (s *Store) FinishGitEvent(ctx context.Context, id, affectsAll string, decisions []api.GitEventStack) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM git_event_stacks WHERE event_id = $1`, id); err != nil {
			return err
		}
		for _, d := range decisions {
			total := len(d.Files)
			files := d.Files
			if total > maxEventFiles {
				files = files[:maxEventFiles]
			}
			var data []byte
			if files != nil {
				var err error
				if data, err = json.Marshal(files); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO git_event_stacks (event_id, stack_id, stack_name, outcome, reason, files, files_total, run_id)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
				id, nullable(d.StackID), d.StackName, d.Outcome, nullable(d.Reason), data, total, nullable(d.RunID)); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `
			UPDATE git_events SET status = 'done', affects_all = $2, error = NULL, processed_at = now() WHERE id = $1`,
			id, nullable(affectsAll))
		return err
	})
}

// RetryGitEvent puts an event back to be tried later, or fails it after too
// many attempts, which lets the branch's next events go on.
func (s *Store) RetryGitEvent(ctx context.Context, id string, cause error) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE git_events SET
			status = CASE WHEN attempts >= $3 THEN 'failed' ELSE 'pending' END,
			processed_at = CASE WHEN attempts >= $3 THEN now() END,
			not_before = now() + make_interval(secs => attempts * 30),
			error = $2
		WHERE id = $1`, id, cause.Error(), gitEventMaxAttempts)
	return err
}

// PurgeGitEvents removes finished events older than the retention (R10.3).
func (s *Store) PurgeGitEvents(ctx context.Context, older time.Duration) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM git_events WHERE status IN ('done', 'failed') AND received_at < now() - make_interval(secs => $1)`,
		older.Seconds())
	return tag.RowsAffected(), err
}

// ListGitEvents returns the recent events, newest first. With stackID, only
// those with a decision about that stack, and only that decision.
func (s *Store) ListGitEvents(ctx context.Context, orgID, stackID string, limit int) ([]api.GitEvent, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT g.id, g.kind, g.source, g.repository, g.ref, g.before_sha, g.after_sha, g.total_commits, g.sender, g.status,
		       g.affects_all, g.error, g.received_at, g.processed_at
		FROM git_events g
		WHERE g.organization_id = $1
		  AND ($2::uuid IS NULL OR EXISTS (SELECT 1 FROM git_event_stacks d WHERE d.event_id = g.id AND d.stack_id = $2::uuid))
		ORDER BY g.seq DESC LIMIT $3`, orgID, nullable(stackID), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.GitEvent{}
	ids := []string{}
	index := map[string]int{}
	for rows.Next() {
		var e api.GitEvent
		var affects, errMsg *string
		if err := rows.Scan(&e.ID, &e.Kind, &e.Source, &e.Repository, &e.Ref, &e.BeforeSHA, &e.AfterSHA, &e.TotalCommits,
			&e.Sender, &e.Status, &affects, &errMsg, &e.ReceivedAt, &e.ProcessedAt); err != nil {
			return nil, err
		}
		e.AffectsAll, e.Error, e.Stacks = deref(affects), deref(errMsg), []api.GitEventStack{}
		index[e.ID] = len(out)
		ids = append(ids, e.ID)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()

	rows, err = s.pool.Query(ctx, `
		SELECT event_id, stack_id, stack_name, outcome, reason, files, files_total, run_id FROM git_event_stacks
		WHERE event_id = ANY($1::uuid[]) AND ($2::uuid IS NULL OR stack_id = $2::uuid)
		ORDER BY stack_name`, ids, nullable(stackID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var d api.GitEventStack
		var event string
		var stack, reason, run *string
		var files []byte
		if err := rows.Scan(&event, &stack, &d.StackName, &d.Outcome, &reason, &files, &d.FilesTotal, &run); err != nil {
			return nil, err
		}
		d.StackID, d.Reason, d.RunID = deref(stack), deref(reason), deref(run)
		if files != nil {
			if err := json.Unmarshal(files, &d.Files); err != nil {
				return nil, err
			}
		}
		out[index[event]].Stacks = append(out[index[event]].Stacks, d)
	}
	return out, rows.Err()
}

// ConnectionStack is a stack cloned through a Git connection.
type ConnectionStack struct {
	ID, Name, Repository, Ref, Path string
}

// StacksOfConnection lists the stacks that clone through a connection.
func (s *Store) StacksOfConnection(ctx context.Context, connectionID string) ([]ConnectionStack, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, repository, ref, path FROM stacks WHERE connection_id = $1 ORDER BY name`, connectionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ConnectionStack
	for rows.Next() {
		var c ConnectionStack
		if err := rows.Scan(&c.ID, &c.Name, &c.Repository, &c.Ref, &c.Path); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GitOnPush returns what a push does on this stack, once inheritance is resolved.
func (s *Store) GitOnPush(ctx context.Context, orgID, stackID string) (string, error) {
	eff, _, err := s.effective(ctx, s.pool, orgID, api.Level{Kind: api.LevelStack, ID: stackID})
	if err != nil {
		return "", err
	}
	return eff.GitOnPush.Value, nil
}

// PushRun is a run started by a push.
type PushRun struct {
	OrganizationID, StackID, EventID string
	Type                             api.RunType
	BeforeSHA, AfterSHA, Actor       string
	PullRequest                      int
	PullRequestURL                   string
}

// CreatePushRun queues the run of a push, tied to its commit (R5.1), and clears
// what the push makes old on the stack: runs of earlier pushes still queued are
// canceled, and those awaiting approval are discarded, because they would hold
// the new one back (R5.4). Runs executing or started by a person are left
// alone. It returns false when this push already has a run from another
// delivery (R5.3).
func (s *Store) CreatePushRun(ctx context.Context, in PushRun) (*api.Run, bool, error) {
	var run *api.Run
	created := false
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// The same lock ClaimRun takes, so no agent picks up a run being superseded.
		var locked bool
		if err := tx.QueryRow(ctx, `SELECT true FROM stacks WHERE organization_id = $1 AND id = $2 FOR UPDATE`,
			in.OrganizationID, in.StackID).Scan(&locked); err != nil {
			return notFound(err)
		}
		var pr *int
		if in.PullRequest != 0 {
			pr = &in.PullRequest
		}
		placement, err := runPlacement(ctx, tx, in.OrganizationID, in.StackID)
		if err != nil {
			return err
		}
		run, err = scanRun(tx.QueryRow(ctx, `
			WITH inserted AS (
				INSERT INTO runs (organization_id, stack_id, type, status, ref, commit_sha, created_by,
				                  trigger_kind, trigger_actor, trigger_event_id, trigger_before_sha, trigger_pull_request, trigger_pull_request_url, placement)
				SELECT st.organization_id, st.id, $2, 'queued', st.ref, $3, $4, 'push', $4, $5, $6, $7, $8, $9::jsonb
				FROM stacks st WHERE st.id = $1
				ON CONFLICT (stack_id, trigger_before_sha, commit_sha) WHERE trigger_kind = 'push' DO NOTHING
				RETURNING *
			)
			SELECT `+runColumns+` FROM inserted r`,
			in.StackID, in.Type, in.AfterSHA, in.Actor, in.EventID, in.BeforeSHA, pr, nullable(in.PullRequestURL), placement))
		if errors.Is(err, ErrNotFound) {
			run, err = scanRun(tx.QueryRow(ctx, `
				SELECT `+runColumns+` FROM runs r
				WHERE r.stack_id = $1 AND r.trigger_kind = 'push' AND r.trigger_before_sha = $2 AND r.commit_sha = $3`,
				in.StackID, in.BeforeSHA, in.AfterSHA))
			if err != nil {
				return err
			}
			// This event made it earlier and is being processed again.
			created = run.Trigger != nil && run.Trigger.EventID == in.EventID
			return nil
		}
		if err != nil {
			return err
		}
		created = true
		superseded := "superseded by " + shortSHA(in.AfterSHA)
		_, err = tx.Exec(ctx, `
			UPDATE runs SET status = CASE status WHEN 'queued' THEN 'canceled' ELSE 'discarded' END,
			       error = $3, finished_at = now(), updated_at = now()
			WHERE stack_id = $1 AND id <> $2 AND trigger_kind = 'push' AND status IN ('queued', 'awaiting_approval')`,
			in.StackID, run.ID, superseded)
		return err
	})
	if err != nil {
		return nil, false, err
	}
	return run, created, nil
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

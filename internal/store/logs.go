package store

import (
	"context"

	"github.com/bsteigleder/firstlume/internal/api"
)

type LogChunk struct {
	ID    int64
	Phase api.Phase
	Data  string
}

// AppendLog stores output from the executor of the given phase. It doubles as a heartbeat.
func (s *Store) AppendLog(ctx context.Context, runID string, phase api.Phase, data string) error {
	tag, err := s.pool.Exec(ctx, `
		WITH alive AS (
			UPDATE runs SET heartbeat_at = now() WHERE id = $1 AND status = $3 RETURNING id
		)
		INSERT INTO run_log_chunks (run_id, phase, data) SELECT id, $2, $4 FROM alive`,
		runID, phase, statusForPhase(phase), data)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	return nil
}

// ListLogs returns chunks after the given cursor, in order.
func (s *Store) ListLogs(ctx context.Context, runID string, after int64, limit int) ([]LogChunk, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, phase, data FROM run_log_chunks WHERE run_id = $1 AND id > $2 ORDER BY id LIMIT $3`,
		runID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LogChunk
	for rows.Next() {
		var c LogChunk
		if err := rows.Scan(&c.ID, &c.Phase, &c.Data); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SaveArtifact stores an encrypted artifact for a run phase that is executing.
func (s *Store) SaveArtifact(ctx context.Context, runID string, phase api.Phase, name string, data []byte) error {
	enc, err := s.keys.Encrypt(data)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO run_artifacts (run_id, name, data_enc)
		SELECT id, $3, $4 FROM runs WHERE id = $1 AND status = $2
		ON CONFLICT (run_id, name) DO UPDATE SET data_enc = EXCLUDED.data_enc, created_at = now()`,
		runID, statusForPhase(phase), name, enc)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	return nil
}

// LoadArtifact returns a decrypted artifact while the run is executing the given phase.
func (s *Store) LoadArtifact(ctx context.Context, runID string, phase api.Phase, name string) ([]byte, error) {
	var enc []byte
	err := s.pool.QueryRow(ctx, `
		SELECT a.data_enc FROM run_artifacts a JOIN runs r ON r.id = a.run_id
		WHERE a.run_id = $1 AND a.name = $3 AND r.status = $2`,
		runID, statusForPhase(phase), name).Scan(&enc)
	if err != nil {
		return nil, notFound(err)
	}
	return s.keys.Decrypt(enc)
}

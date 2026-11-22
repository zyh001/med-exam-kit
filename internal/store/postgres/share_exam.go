package postgres

import (
	"context"
	"encoding/json"

	"github.com/zyh001/med-exam-kit/internal/store"
)

type shareConfigMirror struct {
	Fingerprints []string `json:"fingerprints"`
	Mode         string   `json:"mode"`
	BankIdx      int      `json:"bank_idx"`
	TimeLimit    int      `json:"time_limit"`
	Ts           int64    `json:"ts"`
	ExpiresAt    int64    `json:"expires_at"`
}

// SaveShareTokenJSON persists a JSON-encoded shareConfig to the share_tokens table.

func (s *Store) SaveShareTokenJSON(ctx context.Context, token string, data []byte) error {
	var sf shareConfigMirror
	if err := json.Unmarshal(data, &sf); err != nil {
		return err
	}
	fpsJSON, _ := json.Marshal(sf.Fingerprints)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO share_tokens (token, bank_idx, mode, time_limit, fps, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (token) DO UPDATE
		  SET bank_idx=$2, mode=$3, time_limit=$4, fps=$5, created_at=$6, expires_at=$7`,
		token, sf.BankIdx, sf.Mode, sf.TimeLimit, string(fpsJSON), sf.Ts, sf.ExpiresAt,
	)
	return err
}

// LoadShareTokenJSON loads a share token from the database and returns it as JSON bytes.
// Returns nil if not found.

func (s *Store) LoadShareTokenJSON(ctx context.Context, token string) []byte {
	var sf shareConfigMirror
	var fpsJSON string
	err := s.pool.QueryRow(ctx, `
		SELECT bank_idx, mode, time_limit, fps, created_at, expires_at
		FROM share_tokens WHERE token=$1`, token).
		Scan(&sf.BankIdx, &sf.Mode, &sf.TimeLimit, &fpsJSON, &sf.Ts, &sf.ExpiresAt)
	if err != nil {
		return nil
	}
	if err := json.Unmarshal([]byte(fpsJSON), &sf.Fingerprints); err != nil {
		return nil
	}
	b, _ := json.Marshal(sf)
	return b
}

// DeleteShareToken removes a token from the database.

func (s *Store) DeleteShareToken(ctx context.Context, token string) {
	s.pool.Exec(ctx, `DELETE FROM share_tokens WHERE token=$1`, token) //nolint:errcheck
}

// CleanExpiredShareTokens removes all expired tokens from the database.

func (s *Store) SaveExamSession(ctx context.Context, id string, answersJSON []byte, ts, startedAt int64, timeLimit int, revealedAt int64) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO exam_sessions(id, answers_json, ts, started_at, time_limit, revealed_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT(id) DO UPDATE SET
			answers_json = EXCLUDED.answers_json,
			ts           = EXCLUDED.ts,
			started_at   = EXCLUDED.started_at,
			time_limit   = EXCLUDED.time_limit,
			revealed_at  = EXCLUDED.revealed_at`,
		id, answersJSON, ts, startedAt, timeLimit, revealedAt)
	return err
}

// LoadExamSessions returns all persisted exam session rows.
// Stale-entry pruning is done by the caller (server/exam_persist.go).

func (s *Store) LoadExamSessions(ctx context.Context) ([]store.ExamSessionRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, answers_json, ts, started_at, time_limit, revealed_at
		FROM exam_sessions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.ExamSessionRow
	for rows.Next() {
		var r store.ExamSessionRow
		if err := rows.Scan(&r.ID, &r.AnswersJSON, &r.Ts, &r.StartedAt, &r.TimeLimit, &r.RevealedAt); err != nil {
			continue
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteExamSession removes a single exam session (cleanup after grace window).

func (s *Store) DeleteExamSession(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM exam_sessions WHERE id = $1`, id)
	return err
}

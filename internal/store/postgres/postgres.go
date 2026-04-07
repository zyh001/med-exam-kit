//go:build !nopg
// +build !nopg

// Package postgres provides a PostgreSQL-backed implementation of store.QuestionStore
// and store.ProgressStore using the pgx/v5 driver (pure Go, no CGO).
package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"time"

	_ "embed"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // registers "pgx" driver for database/sql
	"github.com/zyh001/med-exam-kit/internal/models"
	"github.com/zyh001/med-exam-kit/internal/store"
)

//go:embed schema.sql
var schemaSQL string

// Store implements both QuestionStore and ProgressStore against PostgreSQL.
type Store struct {
	pool *pgxpool.Pool
	sqlDB *sql.DB // database/sql wrapper (for ProgressStore.DB())
}

// New connects to PostgreSQL and runs the schema (idempotent).
func New(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("pgstore: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pgstore: ping: %w", err)
	}
	// Run schema
	if _, err := pool.Exec(ctx, schemaSQL); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pgstore: schema: %w", err)
	}
	// Also open via database/sql for backward-compat code
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("pgstore: sql.Open: %w", err)
	}
	log.Println("[pgstore] connected to PostgreSQL ✅")
	return &Store{pool: pool, sqlDB: sqlDB}, nil
}

func (s *Store) Close() error {
	s.pool.Close()
	return s.sqlDB.Close()
}

// ── QuestionStore ──────────────────────────────────────────────────

func (s *Store) ListBanks(ctx context.Context) ([]store.BankMeta, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id,name,source,count,created_at FROM banks ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.BankMeta
	for rows.Next() {
		var m store.BankMeta
		rows.Scan(&m.ID, &m.Name, &m.Source, &m.Count, &m.CreatedAt)
		out = append(out, m)
	}
	return out, nil
}

func (s *Store) FindBank(ctx context.Context, name string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `SELECT id FROM banks WHERE name=$1`, name).Scan(&id)
	if err != nil {
		return -1, nil // not found
	}
	return id, nil
}

func (s *Store) GetBank(ctx context.Context, bankID int64) ([]*models.Question, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT q.id, q.fingerprint, q.name, q.pkg, q.cls, q.unit, q.mode,
		       q.stem, q.shared_opts, q.discuss, q.source_file
		FROM questions q WHERE q.bank_id=$1 ORDER BY q.id`, bankID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var qs []*models.Question
	var qIDs []int64
	for rows.Next() {
		var q models.Question
		var qid int64
		var soJSON []byte
		rows.Scan(&qid, &q.Fingerprint, &q.Name, &q.Pkg, &q.Cls, &q.Unit,
			&q.Mode, &q.Stem, &soJSON, &q.Discuss, &q.SourceFile)
		json.Unmarshal(soJSON, &q.SharedOptions)
		qs = append(qs, &q)
		qIDs = append(qIDs, qid)
	}
	if len(qs) == 0 {
		return nil, nil
	}

	// Load sub-questions in bulk
	sqRows, err := s.pool.Query(ctx, `
		SELECT question_id, position, text, options, answer, discuss, point,
		       rate, error_prone, ai_answer, ai_discuss, ai_confidence, ai_model, ai_status
		FROM sub_questions WHERE question_id = ANY($1) ORDER BY question_id, position`,
		qIDs)
	if err != nil {
		return qs, nil
	}
	defer sqRows.Close()

	qIdx := map[int64]int{}
	for i, id := range qIDs {
		qIdx[id] = i
	}
	for sqRows.Next() {
		var qid int64
		var pos int
		var sq models.SubQuestion
		var optsJSON []byte
		sqRows.Scan(&qid, &pos, &sq.Text, &optsJSON, &sq.Answer, &sq.Discuss,
			&sq.Point, &sq.Rate, &sq.ErrorProne, &sq.AIAnswer, &sq.AIDiscuss,
			&sq.AIConfidence, &sq.AIModel, &sq.AIStatus)
		json.Unmarshal(optsJSON, &sq.Options)
		if i, ok := qIdx[qid]; ok {
			qs[i].SubQuestions = append(qs[i].SubQuestions, sq)
		}
	}
	return qs, nil
}

func (s *Store) ImportBank(ctx context.Context, name, source string, questions []*models.Question) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	// Upsert bank
	var bankID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO banks(name,source,count,created_at)
		VALUES($1,$2,$3,now())
		ON CONFLICT(name) DO UPDATE SET source=EXCLUDED.source, count=EXCLUDED.count, created_at=now()
		RETURNING id`, name, source, len(questions)).Scan(&bankID)
	if err != nil {
		return 0, fmt.Errorf("upsert bank: %w", err)
	}

	// Delete existing questions for this bank
	tx.Exec(ctx, `DELETE FROM questions WHERE bank_id=$1`, bankID)

	// Batch-insert questions + sub-questions
	for _, q := range questions {
		soJSON, _ := json.Marshal(q.SharedOptions)
		var qid int64
		err = tx.QueryRow(ctx, `
			INSERT INTO questions(bank_id,fingerprint,name,pkg,cls,unit,mode,stem,shared_opts,discuss,source_file)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING id`,
			bankID, q.Fingerprint, q.Name, q.Pkg, q.Cls, q.Unit, q.Mode,
			q.Stem, soJSON, q.Discuss, q.SourceFile).Scan(&qid)
		if err != nil {
			return 0, fmt.Errorf("insert question %s: %w", q.Fingerprint, err)
		}
		for i, sq := range q.SubQuestions {
			optsJSON, _ := json.Marshal(sq.Options)
			tx.Exec(ctx, `
				INSERT INTO sub_questions(question_id,position,text,options,answer,discuss,point,
				  rate,error_prone,ai_answer,ai_discuss,ai_confidence,ai_model,ai_status)
				VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
				qid, i, sq.Text, optsJSON, sq.Answer, sq.Discuss, sq.Point,
				sq.Rate, sq.ErrorProne, sq.AIAnswer, sq.AIDiscuss,
				sq.AIConfidence, sq.AIModel, sq.AIStatus)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	log.Printf("[pgstore] imported bank %q: %d questions", name, len(questions))
	return bankID, nil
}

func (s *Store) DeleteBank(ctx context.Context, bankID int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM banks WHERE id=$1`, bankID)
	return err
}

// ── ProgressStore ──────────────────────────────────────────────────

func (s *Store) Init(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, schemaSQL)
	return err
}

func (s *Store) DB() *sql.DB { return nil } // Postgres: no raw *sql.DB exposed

func (s *Store) RecordSession(ctx context.Context, session map[string]any, userID string) error {
	if userID == "" {
		userID = "_legacy"
	}
	unitsJSON, _ := json.Marshal(session["units"])
	_, err := s.pool.Exec(ctx, `
		INSERT INTO sessions(id,user_id,mode,total,correct,wrong,skip,time_sec,sess_date,units,ts)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT(user_id,id) DO NOTHING`,
		fmt.Sprint(session["id"]), userID,
		str(session["mode"]), intV(session["total"]), intV(session["correct"]),
		intV(session["wrong"]), intV(session["skip"]), intV(session["time_sec"]),
		str(session["date"]), unitsJSON, time.Now().UnixMilli())

	// Record attempts
	if items, ok := session["items"].([]any); ok {
		sid := fmt.Sprint(session["id"])
		for _, raw := range items {
			item, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			s.pool.Exec(ctx, `
				INSERT INTO attempts(user_id,fingerprint,session_id,result,mode,unit,ts)
				VALUES($1,$2,$3,$4,$5,$6,$7)`,
				userID, str(item["fingerprint"]), sid,
				intV(item["result"]), str(item["mode"]), str(item["unit"]),
				time.Now().UnixMilli())
			// SM-2
			qual := intV(item["result"])
			if qual >= 0 {
				s.updateSM2Tx(ctx, userID, str(item["fingerprint"]), qual)
			}
		}
	}
	return err
}

func (s *Store) RecordSessionsBatch(ctx context.Context, sessions []map[string]any, userID string) (processed, skipped []string) {
	for _, sess := range sessions {
		sid := fmt.Sprint(sess["id"])
		var exists int
		s.pool.QueryRow(ctx, `SELECT 1 FROM sessions WHERE user_id=$1 AND id=$2`, userID, sid).Scan(&exists)
		if exists == 1 {
			skipped = append(skipped, sid)
			continue
		}
		if err := s.RecordSession(ctx, sess, userID); err == nil {
			processed = append(processed, sid)
		}
	}
	return
}

func (s *Store) DeleteSession(ctx context.Context, sessionID, userID string) bool {
	if userID == "" {
		userID = "_legacy"
	}
	res, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE id=$1 AND user_id=$2`, sessionID, userID)
	return err == nil && res.RowsAffected() > 0
}

func (s *Store) GetDueFingerprints(ctx context.Context, userID string) []string {
	if userID == "" {
		userID = "_legacy"
	}
	today := time.Now().Format("2006-01-02")
	rows, err := s.pool.Query(ctx,
		`SELECT fingerprint FROM sm2 WHERE user_id=$1 AND next_due<=$2 ORDER BY next_due ASC`,
		userID, today)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var fp string
		rows.Scan(&fp)
		out = append(out, fp)
	}
	return out
}

func (s *Store) UpdateSM2(ctx context.Context, userID, fingerprint string, quality int) error {
	return s.updateSM2Tx(ctx, userID, fingerprint, quality)
}

func (s *Store) updateSM2Tx(ctx context.Context, userID, fingerprint string, quality int) error {
	var ef float64 = 2.5
	var interval, reps int
	s.pool.QueryRow(ctx,
		`SELECT ef,interval,reps FROM sm2 WHERE user_id=$1 AND fingerprint=$2`,
		userID, fingerprint).Scan(&ef, &interval, &reps)

	// SM-2 algorithm
	if quality >= 3 {
		if reps == 0 {
			interval = 1
		} else if reps == 1 {
			interval = 6
		} else {
			interval = int(math.Round(float64(interval) * ef))
		}
		reps++
	} else {
		reps = 0
		interval = 1
	}
	ef = ef + (0.1 - float64(5-quality)*(0.08+float64(5-quality)*0.02))
	if ef < 1.3 {
		ef = 1.3
	}
	dueDate := time.Now().AddDate(0, 0, interval).Format("2006-01-02")

	_, err := s.pool.Exec(ctx, `
		INSERT INTO sm2(user_id,fingerprint,ef,interval,reps,next_due,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT(user_id,fingerprint) DO UPDATE
		SET ef=$3,interval=$4,reps=$5,next_due=$6,updated_at=$7`,
		userID, fingerprint, ef, interval, reps, dueDate, time.Now().UnixMilli())
	return err
}

func (s *Store) GetHistory(ctx context.Context, userID string, limit int) []store.HistoryEntry {
	if userID == "" {
		userID = "_legacy"
	}
	if limit <= 0 {
		limit = 30
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id,mode,total,correct,wrong,skip,time_sec,sess_date,units,ts
		FROM sessions WHERE user_id=$1 ORDER BY ts DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []store.HistoryEntry
	for rows.Next() {
		var e store.HistoryEntry
		var unitsJSON []byte
		var ts, total, correct, wrong, skip, timeSec int64
		if err := rows.Scan(&e.ID, &e.Mode, &total, &correct, &wrong,
			&skip, &timeSec, &e.Date, &unitsJSON, &ts); err != nil {
			log.Printf("[pgstore] GetHistory scan error: %v", err)
			continue
		}
		e.Total, e.Correct, e.Wrong, e.Skip, e.TimeSec =
			int(total), int(correct), int(wrong), int(skip), int(timeSec)
		json.Unmarshal(unitsJSON, &e.Units)
		if e.Total > 0 {
			e.Pct = int(math.Round(float64(e.Correct) / float64(e.Total) * 100))
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		log.Printf("[pgstore] GetHistory rows error: %v", err)
	}
	return out
}

func (s *Store) GetOverallStats(ctx context.Context, userID string) store.OverallStats {
	if userID == "" {
		userID = "_legacy"
	}
	var st store.OverallStats
	var sessions int64
	s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM sessions WHERE user_id=$1`, userID).Scan(&sessions)
	st.Sessions = int(sessions)
	var attempts, correct, wrong, skip int64
	s.pool.QueryRow(ctx, `
		SELECT COUNT(*),
		       SUM(CASE WHEN result=1 THEN 1 ELSE 0 END),
		       SUM(CASE WHEN result=0 THEN 1 ELSE 0 END),
		       SUM(CASE WHEN result=-1 THEN 1 ELSE 0 END)
		FROM attempts WHERE user_id=$1`, userID).Scan(
		&attempts, &correct, &wrong, &skip)
	st.Attempts, st.Correct, st.Wrong, st.Skip =
		int(attempts), int(correct), int(wrong), int(skip)
	today := time.Now().Format("2006-01-02")
	var due int64
	s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM sm2 WHERE user_id=$1 AND next_due<=$2`, userID, today).Scan(&due)
	st.DueToday = int(due)
	return st
}

func (s *Store) GetUnitStats(ctx context.Context, userID string) []store.UnitStat {
	if userID == "" {
		userID = "_legacy"
	}
	rows, err := s.pool.Query(ctx, `
		SELECT unit,
		       COUNT(*) AS total,
		       SUM(CASE WHEN result=1 THEN 1 ELSE 0 END) AS correct,
		       SUM(CASE WHEN result=0 THEN 1 ELSE 0 END) AS wrong
		FROM attempts WHERE user_id=$1 AND result!= -1 AND unit IS NOT NULL AND unit!=''
		GROUP BY unit ORDER BY total DESC LIMIT 30`, userID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []store.UnitStat
	for rows.Next() {
		var unit string
		var total, correct, wrong int64
		if err := rows.Scan(&unit, &total, &correct, &wrong); err != nil {
			log.Printf("[pgstore] GetUnitStats scan error: %v", err)
			continue
		}
		u := store.UnitStat{
			Unit: unit, Total: int(total), Correct: int(correct), Wrong: int(wrong),
		}
		if u.Total > 0 {
			u.Accuracy = int(math.Round(float64(u.Correct) / float64(u.Total) * 100))
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		log.Printf("[pgstore] GetUnitStats rows error: %v", err)
	}
	return out
}

func (s *Store) GetWrongFingerprints(ctx context.Context, userID string, limit int) []store.WrongEntry {
	if userID == "" {
		userID = "_legacy"
	}
	if limit <= 0 {
		limit = 300
	}
	rows, err := s.pool.Query(ctx, `
		SELECT fingerprint,
		       COUNT(*) AS total,
		       SUM(CASE WHEN result=1 THEN 1 ELSE 0 END) AS correct,
		       SUM(CASE WHEN result=0 THEN 1 ELSE 0 END) AS wrong
		FROM attempts WHERE user_id=$1 AND result!= -1
		GROUP BY fingerprint HAVING SUM(CASE WHEN result=0 THEN 1 ELSE 0 END)>0
		ORDER BY wrong DESC, MAX(ts) DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []store.WrongEntry
	for rows.Next() {
		var fp string
		var total, correct, wrong int64
		if err := rows.Scan(&fp, &total, &correct, &wrong); err != nil {
			log.Printf("[pgstore] GetWrongFingerprints scan error: %v", err)
			continue
		}
		e := store.WrongEntry{
			Fingerprint: fp,
			Total:       int(total),
			Correct:     int(correct),
			Wrong:       int(wrong),
		}
		if e.Total > 0 {
			e.Accuracy = int(math.Round(float64(e.Correct) / float64(e.Total) * 100))
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		log.Printf("[pgstore] GetWrongFingerprints rows error: %v", err)
	}
	return out
}

func (s *Store) GetSyncStatus(ctx context.Context, userID string) map[string]any {
	if userID == "" {
		userID = "_legacy"
	}
	var cnt int
	var lastTS *int64
	s.pool.QueryRow(ctx,
		`SELECT COUNT(*), MAX(ts) FROM sessions WHERE user_id=$1`, userID).Scan(&cnt, &lastTS)
	return map[string]any{"session_count": cnt, "last_ts": lastTS}
}

func (s *Store) ClearUserData(ctx context.Context, userID string) map[string]int {
	if userID == "" {
		userID = "_legacy"
	}
	counts := map[string]int{}
	for _, tbl := range []string{"attempts", "sessions", "sm2"} {
		res, _ := s.pool.Exec(ctx, fmt.Sprintf("DELETE FROM %s WHERE user_id=$1", tbl), userID)
		counts[tbl] = int(res.RowsAffected())
	}
	return counts
}

func (s *Store) MigrateUserData(ctx context.Context, fromUID, toUID string) (map[string]int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	counts := map[string]int{}
	// sessions
	r, _ := tx.Exec(ctx, `
		INSERT INTO sessions SELECT id,$1,mode,total,correct,wrong,skip,time_sec,sess_date,units,ts
		FROM sessions WHERE user_id=$2 ON CONFLICT DO NOTHING`, toUID, fromUID)
	counts["sessions"] = int(r.RowsAffected())
	// attempts
	r, _ = tx.Exec(ctx, `
		INSERT INTO attempts(user_id,fingerprint,session_id,result,mode,unit,ts)
		SELECT $1,fingerprint,session_id,result,mode,unit,ts FROM attempts WHERE user_id=$2`,
		toUID, fromUID)
	counts["attempts"] = int(r.RowsAffected())
	// sm2
	r, _ = tx.Exec(ctx, `
		INSERT INTO sm2 SELECT $1,fingerprint,ef,interval,reps,next_due,updated_at
		FROM sm2 WHERE user_id=$2 ON CONFLICT DO NOTHING`, toUID, fromUID)
	counts["sm2_cards"] = int(r.RowsAffected())
	// delete source
	for _, tbl := range []string{"attempts", "sessions", "sm2"} {
		tx.Exec(ctx, fmt.Sprintf("DELETE FROM %s WHERE user_id=$1", tbl), fromUID)
	}
	return counts, tx.Commit(ctx)
}

// ── helpers ────────────────────────────────────────────────────────

func str(v any) string {
	if v == nil {
		return ""
	}
	s, _ := v.(string)
	return s
}

func intV(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	}
	return 0
}

// DiagAttempts returns raw diagnostics about the attempts table for a user.
func (s *Store) DiagAttempts(ctx context.Context, userID string) map[string]any {
	out := map[string]any{}

	// 1. Raw counts by result value
	rows, err := s.pool.Query(ctx,
		`SELECT result, COUNT(*) FROM attempts WHERE user_id=$1 GROUP BY result ORDER BY result`,
		userID)
	if err != nil {
		out["result_counts_error"] = err.Error()
	} else {
		defer rows.Close()
		counts := map[string]int64{}
		for rows.Next() {
			var r int64
			var cnt int64
			rows.Scan(&r, &cnt)
			counts[fmt.Sprintf("result_%d", r)] = cnt
		}
		if err := rows.Err(); err != nil {
			out["result_counts_rows_error"] = err.Error()
		}
		out["result_counts"] = counts
	}

	// 2. Sample fingerprints with result=0
	rows2, err2 := s.pool.Query(ctx,
		`SELECT fingerprint, result FROM attempts WHERE user_id=$1 LIMIT 5`, userID)
	if err2 != nil {
		out["sample_error"] = err2.Error()
	} else {
		defer rows2.Close()
		var samples []map[string]any
		for rows2.Next() {
			var fp string
			var r int64
			rows2.Scan(&fp, &r)
			samples = append(samples, map[string]any{"fp": fp, "result": r})
		}
		out["sample_attempts"] = samples
	}

	// 3. Try the exact GetWrongFingerprints query and capture error
	rows3, err3 := s.pool.Query(ctx, `
		SELECT fingerprint, COUNT(*) AS total,
		       SUM(CASE WHEN result=1 THEN 1 ELSE 0 END) AS correct,
		       SUM(CASE WHEN result=0 THEN 1 ELSE 0 END) AS wrong
		FROM attempts WHERE user_id=$1 AND result!= -1
		GROUP BY fingerprint HAVING SUM(CASE WHEN result=0 THEN 1 ELSE 0 END)>0
		LIMIT 5`, userID)
	if err3 != nil {
		out["wrongbook_query_error"] = err3.Error()
	} else {
		defer rows3.Close()
		var wbRows []map[string]any
		for rows3.Next() {
			var fp string
			var total, correct, wrong int64
			if scanErr := rows3.Scan(&fp, &total, &correct, &wrong); scanErr != nil {
				out["wrongbook_scan_error"] = scanErr.Error()
				break
			}
			wbRows = append(wbRows, map[string]any{
				"fp": fp, "total": total, "correct": correct, "wrong": wrong,
			})
		}
		if err := rows3.Err(); err != nil {
			out["wrongbook_rows_error"] = err.Error()
		}
		out["wrongbook_query_rows"] = wbRows
		out["wrongbook_query_count"] = len(wbRows)
	}

	return out
}

// ExecRaw executes an arbitrary SQL statement (used by migration tool).
func (s *Store) ExecRaw(ctx context.Context, sql string, args ...any) (int64, error) {
	res, err := s.pool.Exec(ctx, sql, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected(), nil
}

// LoadBankQuestions returns all questions for use with the quiz server.
// This is equivalent to bank.LoadBank but sources from PostgreSQL.
func (s *Store) LoadBankQuestions(ctx context.Context, bankID int64) ([]*models.Question, error) {
	return s.GetBank(ctx, bankID)
}

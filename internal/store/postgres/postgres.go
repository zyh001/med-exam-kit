//go:build !nopg

// Package postgres provides a PostgreSQL-backed implementation of store.QuestionStore
// and store.ProgressStore using the pgx/v5 driver (pure Go, no CGO).
package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/zyh001/med-exam-kit/internal/logger"

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
	pool  *pgxpool.Pool
	sqlDB *sql.DB // database/sql wrapper (for ProgressStore.DB())
}

// New connects to PostgreSQL and runs the schema (idempotent).

// execSchemaSQL splits the schema into individual statements and runs each
// separately. pgx v5 extended protocol does not support multi-statement Exec.
func execSchemaSQL(ctx context.Context, pool *pgxpool.Pool, sql string) error {
	stmts := splitSQL(sql)
	for _, stmt := range stmts {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := pool.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("stmt %q: %w", stmt[:min(60, len(stmt))], err)
		}
	}
	return nil
}

// splitSQL splits a SQL script on ";" delimiters (skipping those inside strings).
func splitSQL(script string) []string {
	var stmts []string
	var cur strings.Builder
	inQuote := false
	for i := 0; i < len(script); i++ {
		b := script[i]
		if b == byte(39) { // single quote
			inQuote = !inQuote
		}
		if b == byte(59) && !inQuote { // semicolon
			if s := strings.TrimSpace(cur.String()); s != "" {
				stmts = append(stmts, s)
			}
			cur.Reset()
			continue
		}
		cur.WriteByte(b)
	}
	if s := strings.TrimSpace(cur.String()); s != "" {
		stmts = append(stmts, s)
	}
	return stmts
}
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// fixSM2PrimaryKey rebuilds the sm2 primary key to include bank_id if needed.
// Old installs have PK(user_id, fingerprint); new schema requires (user_id, bank_id, fingerprint).
// Runs two plain ALTER TABLE statements so execSchemaSQL's semicolon-splitter is not involved.
func fixSM2PrimaryKey(ctx context.Context, pool *pgxpool.Pool) error {
	var hasBankID bool
	pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM   information_schema.key_column_usage  kcu
			JOIN   information_schema.table_constraints tc
			       ON tc.constraint_name = kcu.constraint_name
			       AND tc.table_name     = kcu.table_name
			WHERE  kcu.table_name    = 'sm2'
			  AND  kcu.column_name   = 'bank_id'
			  AND  tc.constraint_type = 'PRIMARY KEY'
		)`).Scan(&hasBankID)

	if hasBankID {
		return nil // already correct, nothing to do
	}
	logger.Warnf("[pgstore] sm2 primary key missing bank_id — rebuilding...")
	if _, err := pool.Exec(ctx, `ALTER TABLE sm2 DROP CONSTRAINT IF EXISTS sm2_pkey`); err != nil {
		return fmt.Errorf("drop old PK: %w", err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE sm2 ADD PRIMARY KEY (user_id, bank_id, fingerprint)`); err != nil {
		return fmt.Errorf("add new PK: %w", err)
	}
	logger.Infof("[pgstore] sm2 primary key rebuilt: (user_id, bank_id, fingerprint) ✅")
	return nil
}

func New(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("pgstore: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pgstore: ping: %w", err)
	}
	// Run schema — split on ; because pgx v5 extended protocol rejects multi-statement
	if err := execSchemaSQL(ctx, pool, schemaSQL); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pgstore: schema: %w", err)
	}
	// Migration: fix sm2 PRIMARY KEY to include bank_id.
	// Old installs created sm2 with PK(user_id, fingerprint); bank_id was added later
	// via ALTER TABLE ADD COLUMN, leaving the PK unchanged. This caused
	// ON CONFLICT(user_id, bank_id, fingerprint) to fail with SQLSTATE 42P10.
	// Done in Go to avoid splitting issues with $$ dollar-quoting in execSchemaSQL.
	if err := fixSM2PrimaryKey(ctx, pool); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pgstore: sm2 PK migration: %w", err)
	}
	// Also open via database/sql for backward-compat code
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("pgstore: sql.Open: %w", err)
	}
	logger.Infof("[pgstore] connected to PostgreSQL ✅")
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
	if _, err := tx.Exec(ctx, `DELETE FROM questions WHERE bank_id=$1`, bankID); err != nil {
		return 0, fmt.Errorf("delete old questions: %w", err)
	}

	// Batch-insert questions + sub-questions
	for _, q := range questions {
		soJSON, _ := json.Marshal(q.SharedOptions)
		var qid int64
		err = tx.QueryRow(ctx, `
			INSERT INTO questions(bank_id,fingerprint,name,pkg,cls,unit,mode,stem,shared_opts,discuss,source_file)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
			ON CONFLICT(bank_id,fingerprint) DO UPDATE SET
			  name=EXCLUDED.name, pkg=EXCLUDED.pkg, cls=EXCLUDED.cls,
			  unit=EXCLUDED.unit, mode=EXCLUDED.mode, stem=EXCLUDED.stem,
			  shared_opts=EXCLUDED.shared_opts, discuss=EXCLUDED.discuss,
			  source_file=EXCLUDED.source_file
			RETURNING id`,
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
	logger.Infof("[pgstore] imported bank %q: %d questions", name, len(questions))
	return bankID, nil
}

func (s *Store) DeleteBank(ctx context.Context, bankID int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM banks WHERE id=$1`, bankID)
	return err
}

// DeleteBankStats contains the per-table row counts affected by
// DeleteBankWithProgress. Useful for reporting to the caller.
type DeleteBankStats struct {
	Questions    int64 // cascaded from banks deletion
	SubQuestions int64 // cascaded from questions deletion
	Sessions     int64
	Attempts     int64
	SM2          int64
	Favorites    int64
	ShareTokens  int64 // only rows whose fps list referenced this bank — rare, we match by bank_idx if stored
}

// DeleteBankWithProgress removes the bank row (cascading questions/sub_questions
// via ON DELETE CASCADE) and, when keepProgress is false, also purges all
// learning progress rows tagged with the bank ID (sessions, attempts, sm2,
// favorites). The whole operation runs in a single transaction so a partial
// state is impossible.
//
// Returns the row counts per table. If the bank does not exist, returns an
// error and makes no changes.
func (s *Store) DeleteBankWithProgress(ctx context.Context, bankID int64, keepProgress bool) (*DeleteBankStats, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	// Verify bank exists so we can give a clear error instead of "0 rows".
	var name string
	if err := tx.QueryRow(ctx, `SELECT name FROM banks WHERE id=$1`, bankID).Scan(&name); err != nil {
		return nil, fmt.Errorf("题库 #%d 不存在", bankID)
	}

	stats := &DeleteBankStats{}

	// Count questions/sub_questions BEFORE delete so the caller can report
	// what cascaded.
	_ = tx.QueryRow(ctx,
		`SELECT COUNT(*) FROM questions WHERE bank_id=$1`, bankID).Scan(&stats.Questions)
	_ = tx.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM   sub_questions sq
		JOIN   questions q ON q.id = sq.question_id
		WHERE  q.bank_id = $1`, bankID).Scan(&stats.SubQuestions)

	if !keepProgress {
		// Delete progress rows first — the ORDER does not matter because
		// they have no FK to banks/questions. We do it inside the tx so
		// a failure rolls everything back.
		if tag, err := tx.Exec(ctx, `DELETE FROM sessions  WHERE bank_id=$1`, bankID); err != nil {
			return nil, fmt.Errorf("删除 sessions 失败: %w", err)
		} else {
			stats.Sessions = tag.RowsAffected()
		}
		if tag, err := tx.Exec(ctx, `DELETE FROM attempts  WHERE bank_id=$1`, bankID); err != nil {
			return nil, fmt.Errorf("删除 attempts 失败: %w", err)
		} else {
			stats.Attempts = tag.RowsAffected()
		}
		if tag, err := tx.Exec(ctx, `DELETE FROM sm2       WHERE bank_id=$1`, bankID); err != nil {
			return nil, fmt.Errorf("删除 sm2 失败: %w", err)
		} else {
			stats.SM2 = tag.RowsAffected()
		}
		if tag, err := tx.Exec(ctx, `DELETE FROM favorites WHERE bank_id=$1`, bankID); err != nil {
			return nil, fmt.Errorf("删除 favorites 失败: %w", err)
		} else {
			stats.Favorites = tag.RowsAffected()
		}
	}

	// Finally the bank itself (cascades to questions, sub_questions).
	if _, err := tx.Exec(ctx, `DELETE FROM banks WHERE id=$1`, bankID); err != nil {
		return nil, fmt.Errorf("删除 banks 失败: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("提交事务失败: %w", err)
	}
	return stats, nil
}

// AppendBank 向已有题库（by bankID）追加/更新题目，不删除现有题目。
// 返回追加后该题库的总题目数。
func (s *Store) AppendBank(ctx context.Context, bankID int64, questions []*models.Question) (int, error) {
	// 验证题库存在
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM banks WHERE id=$1)`, bankID).Scan(&exists); err != nil || !exists {
		return 0, fmt.Errorf("题库 #%d 不存在，请先用 db import 创建", bankID)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	added, updated := 0, 0
	for _, q := range questions {
		soJSON, _ := json.Marshal(q.SharedOptions)
		var qid int64
		var existed bool
		// 检查是否已有该 fingerprint
		s.pool.QueryRow(ctx, `SELECT id FROM questions WHERE bank_id=$1 AND fingerprint=$2`, bankID, q.Fingerprint).Scan(&qid)
		existed = qid > 0

		err = tx.QueryRow(ctx, `
			INSERT INTO questions(bank_id,fingerprint,name,pkg,cls,unit,mode,stem,shared_opts,discuss,source_file)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
			ON CONFLICT(bank_id,fingerprint) DO UPDATE SET
			  name=EXCLUDED.name, pkg=EXCLUDED.pkg, cls=EXCLUDED.cls,
			  unit=EXCLUDED.unit, mode=EXCLUDED.mode, stem=EXCLUDED.stem,
			  shared_opts=EXCLUDED.shared_opts, discuss=EXCLUDED.discuss,
			  source_file=EXCLUDED.source_file
			RETURNING id`,
			bankID, q.Fingerprint, q.Name, q.Pkg, q.Cls, q.Unit, q.Mode,
			q.Stem, soJSON, q.Discuss, q.SourceFile).Scan(&qid)
		if err != nil {
			return 0, fmt.Errorf("upsert question %s: %w", q.Fingerprint, err)
		}
		// 删除旧小题，重新插入（保证顺序和内容一致）
		tx.Exec(ctx, `DELETE FROM sub_questions WHERE question_id=$1`, qid)
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
		if existed {
			updated++
		} else {
			added++
		}
	}

	// 更新题库的 count
	var total int
	tx.QueryRow(ctx, `SELECT COUNT(*) FROM questions WHERE bank_id=$1`, bankID).Scan(&total)
	tx.Exec(ctx, `UPDATE banks SET count=$1 WHERE id=$2`, total, bankID)

	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	logger.Infof("[pgstore] append bank #%d: +%d new, %d updated, total=%d", bankID, added, updated, total)
	return total, nil
}

// ── ProgressStore ──────────────────────────────────────────────────

func (s *Store) Init(ctx context.Context) error {
	if err := execSchemaSQL(ctx, s.pool, schemaSQL); err != nil {
		return err
	}
	// Log how many legacy rows (bank_id=0) remain after migration
	var legacyAttempts, legacySM2, legacySessions int64
	s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM attempts  WHERE bank_id=0`).Scan(&legacyAttempts)
	s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM sm2       WHERE bank_id=0`).Scan(&legacySM2)
	s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM sessions  WHERE bank_id=0`).Scan(&legacySessions)
	if legacyAttempts+legacySM2+legacySessions > 0 {
		logger.Warnf("[pgstore] ⚠ 仍有 bank_id=0 旧数据: attempts=%d sm2=%d sessions=%d"+
			"\n  可手动执行 SQL 迁移，或使用 med-exam-kit db repair 修复",
			legacyAttempts, legacySM2, legacySessions)
	} else {
		logger.Infof("[pgstore] ✅ 所有学习数据已正确关联 bank_id")
	}
	return nil
}

func (s *Store) DB() *sql.DB { return nil } // Postgres: no raw *sql.DB exposed

func (s *Store) SyncFavorites(ctx context.Context, userID string, bankID int, adds []store.FavItem, removes [][2]any) ([]store.FavItem, error) {
	if userID == "" {
		userID = "_legacy"
	}
	bid := int64(bankID)

	// Apply removes first
	for _, r := range removes {
		fp, _ := r[0].(string)
		si, _ := r[1].(float64)
		if fp == "" {
			continue
		}
		s.pool.Exec(ctx, `DELETE FROM favorites WHERE user_id=$1 AND bank_id=$2 AND fingerprint=$3 AND si=$4`,
			userID, bid, fp, int(si))
	}

	// Upsert adds (last-write-wins via added_at)
	for _, a := range adds {
		if a.Fingerprint == "" {
			continue
		}
		s.pool.Exec(ctx, `
			INSERT INTO favorites(user_id,bank_id,fingerprint,si,added_at)
			VALUES($1,$2,$3,$4,$5)
			ON CONFLICT(user_id,bank_id,fingerprint,si)
			DO UPDATE SET added_at=EXCLUDED.added_at WHERE EXCLUDED.added_at > favorites.added_at`,
			userID, bid, a.Fingerprint, a.SI, a.AddedAt)
	}

	// Return full current list
	rows, err := s.pool.Query(ctx,
		`SELECT fingerprint,si,added_at FROM favorites WHERE user_id=$1 AND bank_id=$2 ORDER BY added_at DESC`,
		userID, bid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.FavItem
	for rows.Next() {
		var item store.FavItem
		rows.Scan(&item.Fingerprint, &item.SI, &item.AddedAt)
		out = append(out, item)
	}
	return out, nil
}

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
func (s *Store) DiagAttempts(ctx context.Context, userID string, bankID int) map[string]any {
	out := map[string]any{
		"user_id": userID,
		"bank_id": bankID,
	}

	// 1. All distinct (user_id, bank_id) combos in attempts — ignores our filter
	rows, err := s.pool.Query(ctx,
		`SELECT user_id, bank_id, COUNT(*), SUM(CASE WHEN result=0 THEN 1 ELSE 0 END) as wrong
		 FROM attempts GROUP BY user_id, bank_id ORDER BY bank_id`)
	if err != nil {
		out["all_groups_error"] = err.Error()
	} else {
		defer rows.Close()
		type row struct {
			UID   string `json:"uid"`
			BID   int64  `json:"bank_id"`
			Cnt   int64  `json:"total"`
			Wrong int64  `json:"wrong"`
		}
		var groups []row
		for rows.Next() {
			var r row
			rows.Scan(&r.UID, &r.BID, &r.Cnt, &r.Wrong)
			groups = append(groups, r)
		}
		out["all_groups"] = groups
	}

	// 2. Exact query used by GetWrongFingerprints with current bankID
	var wbSQL string
	var wbArgs []any
	if bankID == 0 {
		wbSQL = `SELECT fingerprint, COUNT(*), SUM(CASE WHEN result=0 THEN 1 ELSE 0 END) as wrong
			FROM attempts WHERE user_id=$1 AND result != -1
			GROUP BY fingerprint HAVING SUM(CASE WHEN result=0 THEN 1 ELSE 0 END)>0 LIMIT 5`
		wbArgs = []any{userID}
	} else {
		wbSQL = `SELECT fingerprint, COUNT(*), SUM(CASE WHEN result=0 THEN 1 ELSE 0 END) as wrong
			FROM attempts WHERE user_id=$1 AND bank_id=$2 AND result != -1
			GROUP BY fingerprint HAVING SUM(CASE WHEN result=0 THEN 1 ELSE 0 END)>0 LIMIT 5`
		wbArgs = []any{userID, int64(bankID)}
	}
	out["wrongbook_sql"] = wbSQL
	rows2, err2 := s.pool.Query(ctx, wbSQL, wbArgs...)
	if err2 != nil {
		out["wrongbook_error"] = err2.Error()
	} else {
		defer rows2.Close()
		type wb struct {
			FP    string `json:"fp"`
			Total int64  `json:"total"`
			Wrong int64  `json:"wrong"`
		}
		var wbRows []wb
		for rows2.Next() {
			var r wb
			rows2.Scan(&r.FP, &r.Total, &r.Wrong)
			wbRows = append(wbRows, r)
		}
		out["wrongbook_rows"] = wbRows
		out["wrongbook_count"] = len(wbRows)
	}

	// 3. Check if bank_id column exists
	var colExists int
	s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM information_schema.columns
		 WHERE table_name='attempts' AND column_name='bank_id'`).Scan(&colExists)
	out["bank_id_column_exists"] = colExists > 0

	// 4. Sample raw rows
	rows3, err3 := s.pool.Query(ctx,
		`SELECT user_id, bank_id, fingerprint, result FROM attempts WHERE user_id=$1 LIMIT 5`, userID)
	if err3 != nil {
		out["sample_error"] = err3.Error()
	} else {
		defer rows3.Close()
		type sample struct {
			UID    string `json:"uid"`
			BID    int64  `json:"bank_id"`
			FP     string `json:"fp"`
			Result int    `json:"result"`
		}
		var samples []sample
		for rows3.Next() {
			var r sample
			rows3.Scan(&r.UID, &r.BID, &r.FP, &r.Result)
			samples = append(samples, r)
		}
		out["sample_rows"] = samples
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

// ── shareStorer implementation ─────────────────────────────────────────────
// These methods implement the server.shareStorer interface for persisting
// share tokens across server restarts (PG mode only).
// We use []byte (JSON) to avoid an import cycle with the server package.

// shareConfigMirror mirrors server.shareConfig JSON fields.
func (s *Store) CleanExpiredShareTokens(ctx context.Context) {
	s.pool.Exec(ctx, `DELETE FROM share_tokens WHERE expires_at < $1`, time.Now().Unix()) //nolint:errcheck
}

// ── AI Chat Log Storer ─────────────────────────────────────────────────

// SaveAIChatLog persists an AI Q&A conversation log for compliance audit.

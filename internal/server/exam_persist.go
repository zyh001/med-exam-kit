package server

// exam_persist.go — 考试会话持久化
//
// 不再写 JSON 文件，改为存入已有数据库（SQLite 或 PostgreSQL）。
// 表结构非常简单，与 sessions / attempts 等表共处一个 DB。
//
// SQLite：直接用 b.DB（*sql.DB）
// Postgres：通过 b.PgStore，要求其实现 examSessionStorer 接口
//
// 降级策略：
//   - 若所有 bank 均无可用 DB，退回到旧的 JSON 文件（exam-sessions.json），
//     并打印一条警告，保证数据永远不会丢。

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/zyh001/med-exam-kit/internal/logger"
	"github.com/zyh001/med-exam-kit/internal/store"
)

// ────────────────────────────────────────────────────────────────
// Postgres interface (implemented in store/postgres/postgres.go)
// ────────────────────────────────────────────────────────────────

// examSessionStorer is the subset of PgStore used here.
// postgres.Store implements this; noop.Store returns empty/nil.
type examSessionStorer interface {
	SaveExamSession(ctx context.Context, id string, answersJSON []byte, ts, startedAt int64, timeLimit int, revealedAt int64) error
	LoadExamSessions(ctx context.Context) ([]store.ExamSessionRow, error)
	DeleteExamSession(ctx context.Context, id string) error
}

// ────────────────────────────────────────────────────────────────
// SQLite DDL (one-time migration, called from progress.InitDB)
// ────────────────────────────────────────────────────────────────

const examSessionsDDL = `
CREATE TABLE IF NOT EXISTS exam_sessions (
    id           TEXT    PRIMARY KEY,
    answers_json TEXT    NOT NULL DEFAULT '{}',
    ts           INTEGER NOT NULL,
    started_at   INTEGER NOT NULL DEFAULT 0,
    time_limit   INTEGER NOT NULL DEFAULT 0,
    revealed_at  INTEGER NOT NULL DEFAULT 0
);`

// initExamSessionsTable ensures the SQLite table exists.
func initExamSessionsTable(db *sql.DB) error {
	_, err := db.Exec(examSessionsDDL)
	return err
}

// ────────────────────────────────────────────────────────────────
// dbOrNil returns (sqliteDB, pgStorer) for the first available bank.
// Caller must check which is non-nil.
// ────────────────────────────────────────────────────────────────
func (s *Server) dbOrNil() (*sql.DB, examSessionStorer) {
	for i := range s.cfg.Banks {
		b := &s.cfg.Banks[i]
		if b.PgStore != nil {
			if st, ok := b.PgStore.(examSessionStorer); ok {
				return nil, st
			}
		}
		if b.DB != nil {
			return b.DB, nil
		}
	}
	return nil, nil
}

// ────────────────────────────────────────────────────────────────
// Public helpers (called from server.go)
// ────────────────────────────────────────────────────────────────

// persistExamSessions writes all current sessions to the DB (best-effort).
// Run in a goroutine for non-blocking writes; call synchronously before shutdown.
func (s *Server) persistExamSessions() {
	s.examMu.Lock()
	snap := make(map[string]*examSession, len(s.examSessions))
	for id, sess := range s.examSessions {
		snap[id] = sess
	}
	s.examMu.Unlock()

	sqlDB, pgSt := s.dbOrNil()

	if sqlDB == nil && pgSt == nil {
		// No DB available — fall back to JSON file
		s.persistExamSessionsFile(snap)
		return
	}

	ctx := context.Background()
	for id, sess := range snap {
		raw, err := json.Marshal(sess.answers)
		if err != nil {
			logger.Errorf("[exam-persist] 序列化 session %s 失败: %v", id, err)
			continue
		}
		if pgSt != nil {
			if err := pgSt.SaveExamSession(ctx, id, raw, sess.ts, sess.startedAt, sess.timeLimit, sess.revealedAt); err != nil {
				logger.Errorf("[exam-persist] PG 写入 session %s 失败: %v", id, err)
			}
		} else {
			if err := sqliteSaveExamSession(sqlDB, id, raw, sess.ts, sess.startedAt, sess.timeLimit, sess.revealedAt); err != nil {
				logger.Errorf("[exam-persist] SQLite 写入 session %s 失败: %v", id, err)
			}
		}
	}
}

// loadExamSessions reads persisted sessions into memory on startup,
// pruning stale sessions.
func (s *Server) loadExamSessions() {
	sqlDB, pgSt := s.dbOrNil()

	var rows []store.ExamSessionRow
	if pgSt != nil {
		ctx := context.Background()
		// Ensure table exists (Postgres schema migration is idempotent)
		var err error
		rows, err = pgSt.LoadExamSessions(ctx)
		if err != nil {
			logger.Warnf("[exam-persist] PG 读取 exam_sessions 失败: %v", err)
			return
		}
	} else if sqlDB != nil {
		// Ensure SQLite table exists
		if err := initExamSessionsTable(sqlDB); err != nil {
			logger.Warnf("[exam-persist] SQLite 建表失败: %v", err)
			return
		}
		var err error
		rows, err = sqliteLoadExamSessions(sqlDB)
		if err != nil {
			logger.Warnf("[exam-persist] SQLite 读取 exam_sessions 失败: %v", err)
			return
		}
	} else {
		// Fallback: try the JSON file written by an older version
		s.loadExamSessionsFile()
		return
	}

	nowS := time.Now().Unix()
	loaded := 0
	s.examMu.Lock()
	for _, row := range rows {
		if nowS-row.Ts > 86400 {
			continue
		}
		if row.RevealedAt != 0 && nowS-row.RevealedAt > int64(revealGraceWindow) {
			continue
		}
		var answers map[string]examAnswer
		if err := json.Unmarshal(row.AnswersJSON, &answers); err != nil {
			logger.Warnf("[exam-persist] 反序列化 session %s 失败: %v", row.ID, err)
			continue
		}
		s.examSessions[row.ID] = &examSession{
			answers:    answers,
			ts:         row.Ts,
			startedAt:  row.StartedAt,
			timeLimit:  row.TimeLimit,
			revealedAt: row.RevealedAt,
		}
		loaded++
	}
	s.examMu.Unlock()

	if loaded > 0 {
		logger.Infof("[exam-persist] 从数据库恢复 %d 个考试会话", loaded)
	}
}

// activeExamCount returns the number of in-progress (not yet revealed) sessions.
func (s *Server) activeExamCount() int {
	s.examMu.Lock()
	defer s.examMu.Unlock()
	n := 0
	for _, sess := range s.examSessions {
		if sess.revealedAt == 0 {
			n++
		}
	}
	return n
}

// examSessionsSummary returns a human-readable summary for the shutdown warning.
func (s *Server) examSessionsSummary() string {
	s.examMu.Lock()
	defer s.examMu.Unlock()
	active := 0
	for _, sess := range s.examSessions {
		if sess.revealedAt == 0 {
			active++
		}
	}
	return fmt.Sprintf("%d 场考试正在进行中（答案已持久化到数据库，重启后可恢复）", active)
}

// ────────────────────────────────────────────────────────────────
// SQLite helpers
// ────────────────────────────────────────────────────────────────

func sqliteSaveExamSession(db *sql.DB, id string, answersJSON []byte, ts, startedAt int64, timeLimit int, revealedAt int64) error {
	_, err := db.Exec(`
		INSERT INTO exam_sessions(id, answers_json, ts, started_at, time_limit, revealed_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			answers_json = excluded.answers_json,
			ts           = excluded.ts,
			started_at   = excluded.started_at,
			time_limit   = excluded.time_limit,
			revealed_at  = excluded.revealed_at`,
		id, string(answersJSON), ts, startedAt, timeLimit, revealedAt)
	return err
}

func sqliteLoadExamSessions(db *sql.DB) ([]store.ExamSessionRow, error) {
	rows, err := db.Query(`
		SELECT id, answers_json, ts, started_at, time_limit, revealed_at
		FROM exam_sessions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.ExamSessionRow
	for rows.Next() {
		var r store.ExamSessionRow
		var answersStr string
		if err := rows.Scan(&r.ID, &answersStr, &r.Ts, &r.StartedAt, &r.TimeLimit, &r.RevealedAt); err != nil {
			continue
		}
		r.AnswersJSON = []byte(answersStr)
		out = append(out, r)
	}
	return out, rows.Err()
}

// ────────────────────────────────────────────────────────────────
// JSON-file fallback (no DB available — shouldn't normally happen)
// ────────────────────────────────────────────────────────────────

func (s *Server) examSessionsFilePath() string {
	base := "exam-sessions.json"
	if s.configPath != "" {
		return filepath.Join(filepath.Dir(s.configPath), base)
	}
	return base
}

func (s *Server) persistExamSessionsFile(snap map[string]*examSession) {
	logger.Warnf("[exam-persist] 无可用数据库，降级写 JSON 文件")
	type diskEntry struct {
		Answers    map[string]examAnswer `json:"answers"`
		Ts         int64                 `json:"ts"`
		StartedAt  int64                 `json:"started_at"`
		TimeLimit  int                   `json:"time_limit"`
		RevealedAt int64                 `json:"revealed_at"`
	}
	m := make(map[string]diskEntry, len(snap))
	for id, sess := range snap {
		m[id] = diskEntry{sess.answers, sess.ts, sess.startedAt, sess.timeLimit, sess.revealedAt}
	}
	data, _ := json.MarshalIndent(m, "", "  ")
	path := s.examSessionsFilePath()
	tmp := path + ".tmp"
	_ = os.WriteFile(tmp, data, 0600)
	_ = os.Rename(tmp, path)
}

func (s *Server) loadExamSessionsFile() {
	path := s.examSessionsFilePath()
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	type diskEntry struct {
		Answers    map[string]examAnswer `json:"answers"`
		Ts         int64                 `json:"ts"`
		StartedAt  int64                 `json:"started_at"`
		TimeLimit  int                   `json:"time_limit"`
		RevealedAt int64                 `json:"revealed_at"`
	}
	var m map[string]diskEntry
	if err := json.Unmarshal(data, &m); err != nil {
		return
	}
	nowS := time.Now().Unix()
	loaded := 0
	s.examMu.Lock()
	for id, d := range m {
		if nowS-d.Ts > 86400 || (d.RevealedAt != 0 && nowS-d.RevealedAt > int64(revealGraceWindow)) {
			continue
		}
		s.examSessions[id] = &examSession{answers: d.Answers, ts: d.Ts, startedAt: d.StartedAt, timeLimit: d.TimeLimit, revealedAt: d.RevealedAt}
		loaded++
	}
	s.examMu.Unlock()
	if loaded > 0 {
		logger.Infof("[exam-persist] 从 JSON 文件恢复 %d 个考试会话（旧格式）", loaded)
	}
}

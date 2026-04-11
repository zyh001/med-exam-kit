package progress

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const (
	defaultEF  = 2.5
	minEF      = 1.3
	LegacyUser = "_legacy"
)

// DBPathForBank returns the .progress.db path paired with a .mqb file.
func DBPathForBank(bankPath string) string {
	base := bankPath[:len(bankPath)-len(filepath.Ext(bankPath))]
	return base + ".progress.db"
}

// InitDB creates tables and migrates schema (idempotent).
func InitDB(dbPath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		return nil, err
	}

	ddl := `
CREATE TABLE IF NOT EXISTS sessions (
    id        TEXT    NOT NULL,
    user_id   TEXT    NOT NULL DEFAULT '_legacy',
    mode      TEXT,
    total     INTEGER DEFAULT 0,
    correct   INTEGER DEFAULT 0,
    wrong     INTEGER DEFAULT 0,
    skip      INTEGER DEFAULT 0,
    time_sec  INTEGER DEFAULT 0,
    sess_date TEXT,
    units     TEXT    DEFAULT '[]',
    ts        INTEGER NOT NULL,
    PRIMARY KEY (user_id, id)
);
CREATE INDEX IF NOT EXISTS idx_sess_uid ON sessions(user_id);

CREATE TABLE IF NOT EXISTS attempts (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id     TEXT    NOT NULL DEFAULT '_legacy',
    fingerprint TEXT    NOT NULL,
    session_id  TEXT,
    result      INTEGER NOT NULL,
    mode        TEXT,
    unit        TEXT,
    ts          INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_att_fp     ON attempts(fingerprint);
CREATE INDEX IF NOT EXISTS idx_att_ts     ON attempts(ts);
CREATE INDEX IF NOT EXISTS idx_att_uid    ON attempts(user_id);
CREATE INDEX IF NOT EXISTS idx_att_uid_fp ON attempts(user_id, fingerprint);

CREATE TABLE IF NOT EXISTS sm2 (
    user_id     TEXT    NOT NULL DEFAULT '_legacy',
    fingerprint TEXT    NOT NULL,
    ef          REAL    NOT NULL DEFAULT 2.5,
    interval    INTEGER NOT NULL DEFAULT 1,
    reps        INTEGER NOT NULL DEFAULT 0,
    next_due    TEXT    NOT NULL,
    updated_at  INTEGER NOT NULL,
    PRIMARY KEY (user_id, fingerprint)
);`

	if _, err = db.Exec(ddl); err != nil {
		return nil, fmt.Errorf("progress: init schema: %w", err)
	}

	// Migrate older schemas (add user_id column if missing)
	_ = addColumnIfMissing(db, "attempts", "user_id", "TEXT NOT NULL DEFAULT '_legacy'")
	return db, nil
}

func addColumnIfMissing(db *sql.DB, table, col, def string) error {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			continue
		}
		if name == col {
			return nil // already exists
		}
	}
	_, err = db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, col, def))
	return err
}

// ── Write operations ──────────────────────────────────────────────────

// clientDateOrToday returns the client-supplied date from session["date"] if it's a
// valid YYYY-MM-DD string, otherwise falls back to today's server date.
// This fixes a timezone bug: the server runs in UTC but users may be in UTC+8; using
// the client's local date ensures SM-2 next_due is computed relative to the user's day.
func clientDateOrToday(session map[string]any) string {
	if d := strVal(session, "date", ""); d != "" {
		if _, err := time.Parse("2006-01-02", d); err == nil {
			return d
		}
	}
	return time.Now().Format("2006-01-02")
}

// RecordSession writes a full answer session with SM-2 updates.
func RecordSession(db *sql.DB, session map[string]any, userID string) error {
	if userID == "" {
		userID = LegacyUser
	}
	now := time.Now().UnixMilli()
	today := clientDateOrToday(session)

	sid, _ := session["id"].(string)
	if sid == "" {
		return fmt.Errorf("progress: session missing id")
	}

	unitsJSON, _ := json.Marshal(sliceAny(session["units"]))
	mode, _ := session["mode"].(string)

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.Exec(`INSERT OR REPLACE INTO sessions
		(id,user_id,mode,total,correct,wrong,skip,time_sec,sess_date,units,ts)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		sid, userID, mode,
		intVal(session["total"]), intVal(session["correct"]),
		intVal(session["wrong"]), intVal(session["skip"]),
		intVal(session["time_sec"]), strVal(session, "date", today),
		string(unitsJSON), now)
	if err != nil {
		return err
	}

	for _, itemAny := range sliceAny(session["items"]) {
		item, ok := itemAny.(map[string]any)
		if !ok {
			continue
		}
		fp, _ := item["fingerprint"].(string)
		if fp == "" {
			continue
		}
		res := intVal(item["result"])
		itemMode, _ := item["mode"].(string)
		itemUnit, _ := item["unit"].(string)

		_, err = tx.Exec(`INSERT INTO attempts
			(user_id,fingerprint,session_id,result,mode,unit,ts) VALUES (?,?,?,?,?,?,?)`,
			userID, fp, sid, res, itemMode, itemUnit, now)
		if err != nil {
			continue
		}
		if res != -1 {
			quality := 4
			if res == 0 {
				quality = 1
			}
			if qv, ok := item["quality"].(float64); ok {
				quality = int(math.Max(0, math.Min(5, qv)))
			}
			if err := updateSM2(tx, userID, fp, quality, today); err != nil {
				return err
			}
		}
	}

	return tx.Commit()
}

func updateSM2(tx *sql.Tx, userID, fp string, quality int, today string) error {
	var ef float64
	var interval, reps int
	err := tx.QueryRow(
		`SELECT ef, interval, reps FROM sm2 WHERE user_id=? AND fingerprint=?`,
		userID, fp).Scan(&ef, &interval, &reps)
	if err == sql.ErrNoRows {
		ef, interval, reps = defaultEF, 0, 0
	} else if err != nil {
		return err
	}

	if quality < 3 {
		reps = 0
		interval = 0
	} else {
		switch reps {
		case 0:
			interval = 0
		case 1:
			interval = 1
		case 2:
			interval = 6
		default:
			interval = int(math.Round(float64(interval) * ef))
		}
		reps++
	}
	ef = math.Max(minEF, ef+0.1-float64(5-quality)*(0.08+float64(5-quality)*0.02))

	dueDate := daysAhead(today, interval)
	_, err = tx.Exec(`INSERT OR REPLACE INTO sm2
		(user_id,fingerprint,ef,interval,reps,next_due,updated_at) VALUES (?,?,?,?,?,?,?)`,
		userID, fp, ef, interval, reps, dueDate, time.Now().UnixMilli())
	return err
}

// RecordSessionsBatch handles offline sync: skips already-existing sessions.
func RecordSessionsBatch(db *sql.DB, sessions []map[string]any, userID string) (processed, skipped []string) {
	if userID == "" {
		userID = LegacyUser
	}
	now := time.Now().UnixMilli()

	for _, session := range sessions {
		today := clientDateOrToday(session) // use client's local date per-session
		sid, _ := session["id"].(string)
		if sid == "" {
			continue
		}
		var exists int
		_ = db.QueryRow(`SELECT 1 FROM sessions WHERE user_id=? AND id=?`, userID, sid).Scan(&exists)
		if exists == 1 {
			skipped = append(skipped, sid)
			continue
		}

		unitsJSON, _ := json.Marshal(sliceAny(session["units"]))
		mode, _ := session["mode"].(string)
		tx, err := db.Begin()
		if err != nil {
			skipped = append(skipped, sid)
			continue
		}

		_, err = tx.Exec(`INSERT OR IGNORE INTO sessions
			(id,user_id,mode,total,correct,wrong,skip,time_sec,sess_date,units,ts)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			sid, userID, mode,
			intVal(session["total"]), intVal(session["correct"]),
			intVal(session["wrong"]), intVal(session["skip"]),
			intVal(session["time_sec"]), strVal(session, "date", today),
			string(unitsJSON), now)
		if err != nil {
			tx.Rollback()
			skipped = append(skipped, sid)
			continue
		}

		for _, itemAny := range sliceAny(session["items"]) {
			item, ok := itemAny.(map[string]any)
			if !ok {
				continue
			}
			fp, _ := item["fingerprint"].(string)
			if fp == "" {
				continue
			}
			res := intVal(item["result"])
			itemMode, _ := item["mode"].(string)
			itemUnit, _ := item["unit"].(string)
			tx.Exec(`INSERT INTO attempts
				(user_id,fingerprint,session_id,result,mode,unit,ts) VALUES (?,?,?,?,?,?,?)`,
				userID, fp, sid, res, itemMode, itemUnit, now)
			if res != -1 {
				quality := 4
				if res == 0 {
					quality = 1
				}
				if qv, ok := item["quality"].(float64); ok {
					quality = int(math.Max(0, math.Min(5, qv)))
				}
				updateSM2(tx, userID, fp, quality, today)
			}
		}

		if err = tx.Commit(); err != nil {
			skipped = append(skipped, sid)
		} else {
			processed = append(processed, sid)
		}
	}
	return
}

// ClearUserData deletes all records for a user.
func ClearUserData(db *sql.DB, userID string) map[string]int {
	counts := map[string]int{}
	for _, q := range []struct {
		k string
		s string
	}{
		{"attempts", "DELETE FROM attempts WHERE user_id=?"},
		{"sessions", "DELETE FROM sessions WHERE user_id=?"},
		{"sm2_cards", "DELETE FROM sm2 WHERE user_id=?"},
	} {
		res, err := db.Exec(q.s, userID)
		if err == nil {
			n, _ := res.RowsAffected()
			counts[q.k] = int(n)
		}
	}
	return counts
}

// ── Read operations ───────────────────────────────────────────────────

// GetDueFingerprints returns fingerprints whose SM-2 review is due today.
func GetDueFingerprints(db *sql.DB, userID string) []string {
	if userID == "" {
		userID = LegacyUser
	}
	today := time.Now().Format("2006-01-02")
	rows, err := db.Query(
		`SELECT fingerprint FROM sm2 WHERE user_id=? AND next_due<=?
		UNION
		SELECT DISTINCT fingerprint FROM attempts
		WHERE user_id=? AND fingerprint NOT IN (
			SELECT fingerprint FROM sm2 WHERE user_id=?
		)`,
		userID, today, userID, userID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var fps []string
	for rows.Next() {
		var fp string
		if rows.Scan(&fp) == nil {
			fps = append(fps, fp)
		}
	}
	return fps
}

type WrongEntry struct {
	Fingerprint string `json:"fingerprint"`
	Total       int    `json:"total"`
	Correct     int    `json:"correct"`
	Wrong       int    `json:"wrong"`
	Accuracy    int    `json:"accuracy"`
}

// GetWrongFingerprints returns questions with at least one wrong attempt.
func GetWrongFingerprints(db *sql.DB, userID string, limit int) []WrongEntry {
	if userID == "" {
		userID = LegacyUser
	}
	if limit <= 0 {
		limit = 300
	}
	rows, err := db.Query(`
		SELECT fingerprint,
		       COUNT(*) AS total,
		       SUM(CASE WHEN result=1 THEN 1 ELSE 0 END) AS correct,
		       SUM(CASE WHEN result=0 THEN 1 ELSE 0 END) AS wrong,
		       MAX(ts) AS last_ts
		FROM attempts
		WHERE user_id=? AND result!= -1
		GROUP BY fingerprint HAVING wrong>0 AND correct*1.0/(correct+wrong) < 0.8
		ORDER BY wrong DESC, last_ts DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []WrongEntry
	for rows.Next() {
		var e WrongEntry
		var lastTS int64
		rows.Scan(&e.Fingerprint, &e.Total, &e.Correct, &e.Wrong, &lastTS)
		if e.Total > 0 {
			e.Accuracy = int(math.Round(float64(e.Correct) / float64(e.Total) * 100))
		}
		out = append(out, e)
	}
	return out
}

type HistoryEntry struct {
	ID      string   `json:"id"`
	Mode    string   `json:"mode"`
	Total   int      `json:"total"`
	Correct int      `json:"correct"`
	Wrong   int      `json:"wrong"`
	Skip    int      `json:"skip"`
	TimeSec int      `json:"time_sec"`
	Date    string   `json:"date"`
	Units   []string `json:"units"`
	Pct     int      `json:"pct"`
}

// DeleteSession removes a session by id (only if owned by userID).
func DeleteSession(db *sql.DB, sessionID, userID string) bool {
	if userID == "" {
		userID = LegacyUser
	}
	res, err := db.Exec(
		`DELETE FROM sessions WHERE id=? AND user_id=?`, sessionID, userID)
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

// GetHistory returns the most recent sessions for a user.
func GetHistory(db *sql.DB, userID string, limit int) []HistoryEntry {
	if userID == "" {
		userID = LegacyUser
	}
	if limit <= 0 {
		limit = 30
	}
	rows, err := db.Query(`
		SELECT id,mode,total,correct,wrong,skip,time_sec,sess_date,units,ts
		FROM sessions WHERE user_id=? ORDER BY ts DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []HistoryEntry
	for rows.Next() {
		var e HistoryEntry
		var unitsJSON string
		var ts int64
		rows.Scan(&e.ID, &e.Mode, &e.Total, &e.Correct, &e.Wrong,
			&e.Skip, &e.TimeSec, &e.Date, &unitsJSON, &ts)
		json.Unmarshal([]byte(unitsJSON), &e.Units)
		if e.Total > 0 {
			e.Pct = int(math.Round(float64(e.Correct) / float64(e.Total) * 100))
		}
		out = append(out, e)
	}
	return out
}

type UnitStat struct {
	Unit     string `json:"unit"`
	Total    int    `json:"total"`
	Correct  int    `json:"correct"`
	Wrong    int    `json:"wrong"`
	Accuracy int    `json:"accuracy"`
}

// GetUnitStats returns per-unit attempt statistics.
func GetUnitStats(db *sql.DB, userID string) []UnitStat {
	if userID == "" {
		userID = LegacyUser
	}
	rows, err := db.Query(`
		SELECT unit, COUNT(*) AS total,
		       SUM(CASE WHEN result=1 THEN 1 ELSE 0 END) AS correct
		FROM attempts
		WHERE user_id=? AND result!= -1 AND unit IS NOT NULL AND unit!=''
		GROUP BY unit ORDER BY total DESC`, userID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []UnitStat
	for rows.Next() {
		var s UnitStat
		rows.Scan(&s.Unit, &s.Total, &s.Correct)
		s.Wrong = s.Total - s.Correct
		if s.Total > 0 {
			s.Accuracy = int(math.Round(float64(s.Correct) / float64(s.Total) * 100))
		}
		out = append(out, s)
	}
	return out
}

type OverallStats struct {
	TotalAttempts int `json:"total_attempts"`
	Correct       int `json:"correct"`
	WrongAttempts int `json:"wrong_attempts"`
	Accuracy      int `json:"accuracy"`
	Sessions      int `json:"sessions"`
	DueToday      int `json:"due_today"`
	WrongTopics   int `json:"wrong_topics"`
}

// GetOverallStats returns aggregate statistics.
func GetOverallStats(db *sql.DB, userID string) OverallStats {
	if userID == "" {
		userID = LegacyUser
	}
	today := time.Now().Format("2006-01-02")

	var s OverallStats
	var total, correct, wrong sql.NullInt64
	db.QueryRow(`SELECT COUNT(*),
		SUM(CASE WHEN result=1 THEN 1 ELSE 0 END),
		SUM(CASE WHEN result=0 THEN 1 ELSE 0 END)
		FROM attempts WHERE user_id=? AND result!= -1`, userID).
		Scan(&total, &correct, &wrong)

	s.TotalAttempts = int(total.Int64)
	s.Correct = int(correct.Int64)
	s.WrongAttempts = int(wrong.Int64)
	if s.TotalAttempts > 0 {
		s.Accuracy = int(math.Round(float64(s.Correct) / float64(s.TotalAttempts) * 100))
	}
	db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE user_id=?`, userID).Scan(&s.Sessions)
	db.QueryRow(`SELECT COUNT(*) FROM (
		SELECT fingerprint FROM sm2 WHERE user_id=? AND next_due<=?
		UNION
		SELECT DISTINCT fingerprint FROM attempts
		WHERE user_id=? AND fingerprint NOT IN (
			SELECT fingerprint FROM sm2 WHERE user_id=?
		)
	)`, userID, today, userID, userID).Scan(&s.DueToday)
	db.QueryRow(`SELECT COUNT(DISTINCT fingerprint) FROM attempts WHERE user_id=? AND result=0`, userID).Scan(&s.WrongTopics)
	return s
}

// GetSyncStatus returns session count and last sync timestamp.
func GetSyncStatus(db *sql.DB, userID string) map[string]any {
	if userID == "" {
		userID = LegacyUser
	}
	var cnt int
	var lastTS sql.NullInt64
	db.QueryRow(`SELECT COUNT(*), MAX(ts) FROM sessions WHERE user_id=?`, userID).Scan(&cnt, &lastTS)
	return map[string]any{"session_count": cnt, "last_ts": lastTS.Int64}
}

// ── helpers ────────────────────────────────────────────────────────────

func intVal(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return 0
}

func strVal(m map[string]any, key, def string) string {
	if v, ok := m[key].(string); ok && v != "" {
		return v
	}
	return def
}

func sliceAny(v any) []any {
	if arr, ok := v.([]any); ok {
		return arr
	}
	return nil
}

func daysAhead(today string, days int) string {
	t, err := time.Parse("2006-01-02", today)
	if err != nil {
		t = time.Now()
	}
	return t.AddDate(0, 0, days).Format("2006-01-02")
}

// MigrateUserData copies all records from fromUID to toUID, skipping conflicts,
// then deletes the source rows. Returns counts of migrated rows per table.
func MigrateUserData(db *sql.DB, fromUID, toUID string) (map[string]int, error) {
	if fromUID == "" || toUID == "" || fromUID == toUID {
		return nil, fmt.Errorf("无效的用户 ID")
	}
	counts := map[string]int{}

	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// sessions: PRIMARY KEY (user_id, id) — INSERT OR IGNORE 跳过重复
	res, err := tx.Exec(
		`INSERT OR IGNORE INTO sessions (id,user_id,mode,total,correct,wrong,skip,time_sec,sess_date,units,ts)
		 SELECT id,?,mode,total,correct,wrong,skip,time_sec,sess_date,units,ts FROM sessions WHERE user_id=?`,
		toUID, fromUID)
	if err != nil {
		return nil, fmt.Errorf("migrate sessions: %w", err)
	}
	n, _ := res.RowsAffected()
	counts["sessions"] = int(n)

	// attempts: autoincrement PK, no conflict possible
	res, err = tx.Exec(
		`INSERT INTO attempts (user_id,fingerprint,session_id,result,mode,unit,ts)
		 SELECT ?,fingerprint,session_id,result,mode,unit,ts FROM attempts WHERE user_id=?`,
		toUID, fromUID)
	if err != nil {
		return nil, fmt.Errorf("migrate attempts: %w", err)
	}
	n, _ = res.RowsAffected()
	counts["attempts"] = int(n)

	// sm2: PRIMARY KEY (user_id, fingerprint) — toUID 已有的保留（不覆盖）
	res, err = tx.Exec(
		`INSERT OR IGNORE INTO sm2 (user_id,fingerprint,ef,interval,reps,next_due,updated_at)
		 SELECT ?,fingerprint,ef,interval,reps,next_due,updated_at FROM sm2 WHERE user_id=?`,
		toUID, fromUID)
	if err != nil {
		return nil, fmt.Errorf("migrate sm2: %w", err)
	}
	n, _ = res.RowsAffected()
	counts["sm2_cards"] = int(n)

	// 清除源数据
	for _, q := range []string{
		`DELETE FROM attempts WHERE user_id=?`,
		`DELETE FROM sessions WHERE user_id=?`,
		`DELETE FROM sm2     WHERE user_id=?`,
	} {
		if _, err = tx.Exec(q, fromUID); err != nil {
			return nil, err
		}
	}

	return counts, tx.Commit()
}

// ── Bank-scoped SQLite queries (filter by fingerprint set) ─────────────

// GetOverallStatsByFP returns stats filtered to a specific set of fingerprints.
// Used in SQLite mode to isolate stats per bank (SQLite has no bank_id column).
// clientDate is the caller's local date (YYYY-MM-DD); pass "" to use server date.
func GetOverallStatsByFP(db *sql.DB, userID string, fps []string, clientDate string) OverallStats {
	if userID == "" {
		userID = LegacyUser
	}
	var s OverallStats
	if len(fps) == 0 {
		return s
	}
	today := clientDate
	if today == "" || len(today) != 10 {
		today = time.Now().Format("2006-01-02")
	}
	placeholders := make([]string, len(fps))
	args := make([]any, 0, len(fps)*2+2)
	args = append(args, userID)
	for i, fp := range fps {
		placeholders[i] = "?"
		args = append(args, fp)
	}
	inClause := strings.Join(placeholders, ",")

	var total, correct, wrong sql.NullInt64
	db.QueryRow(`SELECT COUNT(*),
		SUM(CASE WHEN result=1 THEN 1 ELSE 0 END),
		SUM(CASE WHEN result=0 THEN 1 ELSE 0 END)
		FROM attempts WHERE user_id=? AND result!=-1
		AND fingerprint IN (`+inClause+`)`, args...).
		Scan(&total, &correct, &wrong)

	s.TotalAttempts = int(total.Int64)
	s.Correct = int(correct.Int64)
	s.WrongAttempts = int(wrong.Int64)
	if s.TotalAttempts > 0 {
		s.Accuracy = int(math.Round(float64(s.Correct) / float64(s.TotalAttempts) * 100))
	}

	args2 := append([]any{userID}, args[1:]...)
	db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE user_id=?`, userID).Scan(&s.Sessions)
	// DueToday: sm2 due items + items with attempts but no sm2 record (legacy data)
	dueArgs := make([]any, 0, len(fps)*3+3)
	dueArgs = append(dueArgs, userID, today)
	for _, fp := range fps { dueArgs = append(dueArgs, fp) }
	dueArgs = append(dueArgs, userID)
	for _, fp := range fps { dueArgs = append(dueArgs, fp) }
	dueArgs = append(dueArgs, userID)
	for _, fp := range fps { dueArgs = append(dueArgs, fp) }
	db.QueryRow(`SELECT COUNT(*) FROM (
		SELECT fingerprint FROM sm2 WHERE user_id=? AND next_due<=?
		AND fingerprint IN (`+inClause+`)
		UNION
		SELECT DISTINCT fingerprint FROM attempts
		WHERE user_id=? AND fingerprint IN (`+inClause+`)
		AND fingerprint NOT IN (
			SELECT fingerprint FROM sm2 WHERE user_id=? AND fingerprint IN (`+inClause+`)
		)
	)`, dueArgs...).Scan(&s.DueToday)
	_ = args2
	db.QueryRow(`SELECT COUNT(DISTINCT fingerprint) FROM attempts WHERE user_id=? AND result=0
		AND fingerprint IN (`+inClause+`)`, args...).Scan(&s.WrongTopics)
	return s
}

// GetUnitStatsByFP returns unit stats filtered to a fingerprint set.
func GetUnitStatsByFP(db *sql.DB, userID string, fps []string) []UnitStat {
	if userID == "" {
		userID = LegacyUser
	}
	if len(fps) == 0 {
		return nil
	}
	placeholders := make([]string, len(fps))
	args := make([]any, 0, len(fps)+1)
	args = append(args, userID)
	for i, fp := range fps {
		placeholders[i] = "?"
		args = append(args, fp)
	}
	inClause := strings.Join(placeholders, ",")

	rows, err := db.Query(`
		SELECT unit, COUNT(*) AS total,
		       SUM(CASE WHEN result=1 THEN 1 ELSE 0 END) AS correct
		FROM attempts
		WHERE user_id=? AND result!=-1 AND unit IS NOT NULL AND unit!=''
		AND fingerprint IN (`+inClause+`)
		GROUP BY unit ORDER BY total DESC`, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []UnitStat
	for rows.Next() {
		var s UnitStat
		rows.Scan(&s.Unit, &s.Total, &s.Correct)
		s.Wrong = s.Total - s.Correct
		if s.Total > 0 {
			s.Accuracy = int(math.Round(float64(s.Correct) / float64(s.Total) * 100))
		}
		out = append(out, s)
	}
	return out
}

// GetHistoryByFP returns sessions filtered to those containing at least one
// attempt on the given fingerprints.
func GetHistoryByFP(db *sql.DB, userID string, fps []string, limit int) []HistoryEntry {
	if userID == "" {
		userID = LegacyUser
	}
	if len(fps) == 0 {
		return nil
	}
	if limit <= 0 {
		limit = 30
	}
	placeholders := make([]string, len(fps))
	args := make([]any, 0, len(fps)+2)
	args = append(args, userID)
	for i, fp := range fps {
		placeholders[i] = "?"
		args = append(args, fp)
	}
	args = append(args, limit)
	inClause := strings.Join(placeholders, ",")

	rows, err := db.Query(`
		SELECT s.id,s.mode,s.total,s.correct,s.wrong,s.skip,s.time_sec,s.sess_date,s.units,s.ts
		FROM sessions s
		WHERE s.user_id=?
		  AND EXISTS (
		      SELECT 1 FROM attempts a
		      WHERE a.session_id=s.id AND a.user_id=s.user_id
		        AND a.fingerprint IN (`+inClause+`)
		  )
		ORDER BY s.ts DESC LIMIT ?`, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []HistoryEntry
	for rows.Next() {
		var e HistoryEntry
		var unitsJSON string
		var ts int64
		rows.Scan(&e.ID, &e.Mode, &e.Total, &e.Correct, &e.Wrong,
			&e.Skip, &e.TimeSec, &e.Date, &unitsJSON, &ts)
		json.Unmarshal([]byte(unitsJSON), &e.Units)
		if e.Total > 0 {
			e.Pct = int(math.Round(float64(e.Correct) / float64(e.Total) * 100))
		}
		out = append(out, e)
	}
	return out
}

// GetWrongFingerprintsByFP returns wrong entries filtered to a fingerprint set.
func GetWrongFingerprintsByFP(db *sql.DB, userID string, fps []string, limit int) []WrongEntry {
	if userID == "" {
		userID = LegacyUser
	}
	if len(fps) == 0 {
		return nil
	}
	if limit <= 0 {
		limit = 300
	}
	placeholders := make([]string, len(fps))
	args := make([]any, 0, len(fps)+2)
	args = append(args, userID)
	for i, fp := range fps {
		placeholders[i] = "?"
		args = append(args, fp)
	}
	args = append(args, limit)
	inClause := strings.Join(placeholders, ",")

	rows, err := db.Query(`
		SELECT fingerprint,
		       COUNT(*) AS total,
		       SUM(CASE WHEN result=1 THEN 1 ELSE 0 END) AS correct,
		       SUM(CASE WHEN result=0 THEN 1 ELSE 0 END) AS wrong,
		       MAX(ts) AS last_ts
		FROM attempts
		WHERE user_id=? AND result!=-1
		AND fingerprint IN (`+inClause+`)
		GROUP BY fingerprint HAVING wrong>0 AND correct*1.0/(correct+wrong) < 0.8
		ORDER BY wrong DESC, last_ts DESC LIMIT ?`, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []WrongEntry
	for rows.Next() {
		var e WrongEntry
		var lastTS int64
		rows.Scan(&e.Fingerprint, &e.Total, &e.Correct, &e.Wrong, &lastTS)
		if e.Total > 0 {
			e.Accuracy = int(math.Round(float64(e.Correct) / float64(e.Total) * 100))
		}
		out = append(out, e)
	}
	return out
}

// GetDueFingerprintsByFP returns SM-2 due fingerprints filtered to a set.
// clientDate is the caller's local date (YYYY-MM-DD); pass "" to use server date.
func GetDueFingerprintsByFP(db *sql.DB, userID string, fps []string, clientDate string) []string {
	if userID == "" {
		userID = LegacyUser
	}
	if len(fps) == 0 {
		return nil
	}
	today := clientDate
	if today == "" || len(today) != 10 {
		today = time.Now().Format("2006-01-02")
	}
	placeholders := make([]string, len(fps))
	for i := range fps {
		placeholders[i] = "?"
	}
	inClause := strings.Join(placeholders, ",")
	// Build args: userID, today, fps... (for first SELECT), then userID, fps... (for UNION)
	args := make([]any, 0, len(fps)*2+3)
	args = append(args, userID, today)
	for _, fp := range fps {
		args = append(args, fp)
	}
	args = append(args, userID)
	for _, fp := range fps {
		args = append(args, fp)
	}
	args = append(args, userID)
	for _, fp := range fps {
		args = append(args, fp)
	}

	rows, err := db.Query(`
		SELECT fingerprint FROM sm2
		WHERE user_id=? AND next_due<=?
		AND fingerprint IN (`+inClause+`)
		UNION
		SELECT DISTINCT fingerprint FROM attempts
		WHERE user_id=? AND fingerprint IN (`+inClause+`)
		AND fingerprint NOT IN (
			SELECT fingerprint FROM sm2 WHERE user_id=? AND fingerprint IN (`+inClause+`)
		)`, args...)
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

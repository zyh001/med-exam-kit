package progress

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
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
);

CREATE TABLE IF NOT EXISTS ai_chat_logs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id     TEXT    NOT NULL DEFAULT '_legacy',
    fingerprint TEXT    NOT NULL,
    sq_index    INTEGER NOT NULL DEFAULT 0,
    user_answer TEXT    DEFAULT '',
    prompt      TEXT    NOT NULL DEFAULT '[]',
    history_in  TEXT    DEFAULT '[]',
    response    TEXT    DEFAULT '',
    reasoning   TEXT    DEFAULT '',
    truncated   INTEGER DEFAULT 0,
    model       TEXT    NOT NULL,
    provider    TEXT    DEFAULT '',
    created_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_acl_created ON ai_chat_logs(created_at);

CREATE TABLE IF NOT EXISTS exam_sessions (
    id           TEXT    PRIMARY KEY,
    answers_json TEXT    NOT NULL DEFAULT '{}',
    ts           INTEGER NOT NULL,
    started_at   INTEGER NOT NULL DEFAULT 0,
    time_limit   INTEGER NOT NULL DEFAULT 0,
    revealed_at  INTEGER NOT NULL DEFAULT 0
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
		interval = 1 // 答错：明天再复习（原为 0，会导致当天反复出现）
	} else {
		switch reps {
		case 0:
			interval = 1 // 第1次答对：明天复习
		case 1:
			interval = 6 // 第2次答对：6天后
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

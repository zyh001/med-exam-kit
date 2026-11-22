package progress

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/zyh001/med-exam-kit/internal/ai"
)

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

func CleanupStaleUsers(db *sql.DB, maxAgeDays int) (users int, rows int) {
	if maxAgeDays <= 0 {
		maxAgeDays = 7
	}
	cutoff := time.Now().Add(-time.Duration(maxAgeDays) * 24 * time.Hour).UnixMilli()

	// Find stale user IDs (last activity older than cutoff)
	staleRows, err := db.Query(`
		SELECT user_id, MAX(ts) AS last_ts FROM (
			SELECT user_id, ts FROM sessions
			UNION ALL
			SELECT user_id, ts FROM attempts
		) GROUP BY user_id
		HAVING last_ts < ? AND user_id != ?`, cutoff, LegacyUser)
	if err != nil {
		return 0, 0
	}
	defer staleRows.Close()

	var staleUIDs []string
	for staleRows.Next() {
		var uid string
		var lastTS int64
		staleRows.Scan(&uid, &lastTS)
		staleUIDs = append(staleUIDs, uid)
	}

	for _, uid := range staleUIDs {
		counts := ClearUserData(db, uid)
		total := 0
		for _, n := range counts {
			total += n
		}
		rows += total
		users++
	}
	return
}

// ── AI Chat Logs ────────────────────────────────────────────────────────

// SaveAIChatLog persists an AI Q&A conversation log for compliance audit.

func SaveAIChatLog(db *sql.DB, userID string, fp string, sqIdx int, userAnswer string,
	prompt []ai.ChatMessage, history []ai.ChatMessage,
	response string, reasoning string, truncated bool,
	model string, provider string) error {

	promptJSON, _ := json.Marshal(prompt)
	historyJSON, _ := json.Marshal(history)
	truncatedInt := 0
	if truncated {
		truncatedInt = 1
	}

	_, err := db.Exec(`
		INSERT INTO ai_chat_logs(user_id,fingerprint,sq_index,user_answer,
			prompt,history_in,response,reasoning,truncated,model,provider,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		userID, fp, sqIdx, userAnswer,
		string(promptJSON), string(historyJSON),
		response, reasoning, truncatedInt, model, provider,
		time.Now().UnixMilli())
	return err
}

// CleanupAIChatLogs deletes logs older than retentionDays. Returns deleted count.

func CleanupAIChatLogs(db *sql.DB, retentionDays int) (int64, error) {
	if retentionDays <= 0 {
		retentionDays = 30
	}
	cutoff := time.Now().Add(-time.Duration(retentionDays) * 24 * time.Hour).UnixMilli()
	res, err := db.Exec(`DELETE FROM ai_chat_logs WHERE created_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

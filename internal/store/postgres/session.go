package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/zyh001/med-exam-kit/internal/ai"
	"github.com/zyh001/med-exam-kit/internal/logger"
	"github.com/zyh001/med-exam-kit/internal/store"
)

func (s *Store) RecordSession(ctx context.Context, session map[string]any, userID string) error {
	if userID == "" {
		userID = "_legacy"
	}
	unitsRaw, _ := json.Marshal(session["units"])
	unitsStr := string(unitsRaw)
	bankID := intV(session["bank_id"]) // 0 if not provided (backward compat)
	clientDate := str(session["date"]) // client's local date for SM-2 next_due
	_, err := s.pool.Exec(ctx, `
		INSERT INTO sessions(id,user_id,bank_id,mode,total,correct,wrong,skip,time_sec,sess_date,units,ts)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT DO NOTHING`,
		fmt.Sprint(session["id"]), userID, bankID,
		str(session["mode"]), intV(session["total"]), intV(session["correct"]),
		intV(session["wrong"]), intV(session["skip"]), intV(session["time_sec"]),
		clientDate, unitsStr, time.Now().UnixMilli())

	// Record attempts
	if items, ok := session["items"].([]any); ok {
		sid := fmt.Sprint(session["id"])
		for _, raw := range items {
			item, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if _, attErr := s.pool.Exec(ctx, `
				INSERT INTO attempts(user_id,bank_id,fingerprint,session_id,result,mode,unit,ts)
				VALUES($1,$2,$3,$4,$5,$6,$7,$8)`,
				userID, bankID, str(item["fingerprint"]), sid,
				intV(item["result"]), str(item["mode"]), str(item["unit"]),
				time.Now().UnixMilli()); attErr != nil {
				logger.Errorf("[pgstore] attempts insert failed uid=%s fp=%s: %v", userID, str(item["fingerprint"]), attErr)
			}
			// SM-2: map result to quality (must match SQLite logic in progress.go)
			res := intV(item["result"])
			if res != -1 {
				quality := 4 // correct → good recall
				if res == 0 {
					quality = 1 // wrong → poor recall
				}
				if qv, ok := item["quality"].(float64); ok {
					quality = int(math.Max(0, math.Min(5, qv)))
				}
				if sm2Err := s.updateSM2Tx(ctx, userID, bankID, str(item["fingerprint"]), quality, clientDate); sm2Err != nil {
					logger.Errorf("[pgstore] SM-2 update failed uid=%s bank=%d fp=%s: %v", userID, bankID, str(item["fingerprint"]), sm2Err)
				}
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

func (s *Store) GetDueFingerprints(ctx context.Context, userID string, bankID int, clientDate string) []string {
	if userID == "" {
		userID = "_legacy"
	}
	today := clientDate
	if today == "" || len(today) != 10 {
		today = time.Now().Format("2006-01-02")
	}
	// 主查询：SM-2 到期题目
	// 兜底：有 attempts 记录但从未写入 SM-2（如旧数据、写入失败）的题目也纳入复习，
	// 与 SQLite 版 GetDueFingerprintsByFP 行为保持一致。
	rows, err := s.pool.Query(ctx, `
		SELECT fingerprint FROM sm2
		WHERE user_id=$1 AND ($2::bigint=0 OR bank_id=$2) AND next_due<=$3
		UNION
		SELECT DISTINCT fingerprint FROM attempts
		WHERE user_id=$1 AND ($2::bigint=0 OR bank_id=$2) AND result != -1
		  AND fingerprint NOT IN (
		      SELECT fingerprint FROM sm2
		      WHERE user_id=$1 AND ($2::bigint=0 OR bank_id=$2)
		  )
		ORDER BY 1`,
		userID, int64(bankID), today)
	if err != nil {
		logger.Errorf("[pgstore] GetDueFingerprints query failed uid=%s bank=%d: %v", userID, bankID, err)
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
	return s.updateSM2Tx(ctx, userID, 0, fingerprint, quality, "")
}

func (s *Store) updateSM2Tx(ctx context.Context, userID string, bankID int, fingerprint string, quality int, clientDate string) error {
	var ef float64 = 2.5
	var interval, reps int
	s.pool.QueryRow(ctx,
		`SELECT ef,interval,reps FROM sm2 WHERE user_id=$1 AND bank_id=$2 AND fingerprint=$3`,
		userID, bankID, fingerprint).Scan(&ef, &interval, &reps)

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
	// Use client's local date as the base for next_due calculation to avoid timezone mismatch.
	today := clientDate
	if today == "" || len(today) != 10 {
		today = time.Now().Format("2006-01-02")
	}
	base, err := time.Parse("2006-01-02", today)
	if err != nil {
		base = time.Now()
	}
	dueDate := base.AddDate(0, 0, interval).Format("2006-01-02")

	_, err = s.pool.Exec(ctx, `
		INSERT INTO sm2(user_id,bank_id,fingerprint,ef,interval,reps,next_due,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT(user_id,bank_id,fingerprint) DO UPDATE
		SET ef=$4,interval=$5,reps=$6,next_due=$7,updated_at=$8`,
		userID, bankID, fingerprint, ef, interval, reps, dueDate, time.Now().UnixMilli())
	return err
}

func (s *Store) GetHistory(ctx context.Context, userID string, bankID int, limit int) []store.HistoryEntry {
	if userID == "" {
		userID = "_legacy"
	}
	if limit <= 0 {
		limit = 30
	}
	var histSQL string
	var histArgs []any
	if bankID == 0 {
		histSQL = `SELECT id,mode,total,correct,wrong,skip,time_sec,sess_date,units,ts
			FROM sessions WHERE user_id=$1 ORDER BY ts DESC LIMIT $2`
		histArgs = []any{userID, limit}
	} else {
		histSQL = `SELECT id,mode,total,correct,wrong,skip,time_sec,sess_date,units,ts
			FROM sessions WHERE user_id=$1 AND bank_id=$2 ORDER BY ts DESC LIMIT $3`
		histArgs = []any{userID, bankID, limit}
	}
	rows, err := s.pool.Query(ctx, histSQL, histArgs...)
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
			logger.Errorf("[pgstore] GetHistory scan error: %v", err)
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
		logger.Errorf("[pgstore] GetHistory rows error: %v", err)
	}
	return out
}

func (s *Store) GetOverallStats(ctx context.Context, userID string, bankID int, clientDate string) store.OverallStats {
	if userID == "" {
		userID = "_legacy"
	}
	var st store.OverallStats
	var sessions int64
	s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM sessions WHERE user_id=$1 AND ($2::bigint=0 OR bank_id=$2)`, userID, int64(bankID)).Scan(&sessions)
	st.Sessions = int(sessions)
	var attempts, correct, wrong, skip int64
	s.pool.QueryRow(ctx, `
		SELECT COUNT(*),
		       COALESCE(SUM(CASE WHEN result=1 THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN result=0 THEN 1 ELSE 0 END), 0),
		       0
		FROM attempts WHERE user_id=$1 AND ($2::bigint=0 OR bank_id=$2) AND result != -1`, userID, int64(bankID)).Scan(
		&attempts, &correct, &wrong, &skip)
	st.Attempts, st.Correct, st.Wrong, st.Skip =
		int(attempts), int(correct), int(wrong), int(skip)
	today := clientDate
	if today == "" || len(today) != 10 {
		today = time.Now().Format("2006-01-02")
	}
	var due int64
	// 与 GetDueFingerprints 保持一致：SM-2 到期 + 有 attempts 但无 SM-2 记录的兜底
	s.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM (
		    SELECT fingerprint FROM sm2
		    WHERE user_id=$1 AND ($2::bigint=0 OR bank_id=$2) AND next_due<=$3
		    UNION
		    SELECT DISTINCT fingerprint FROM attempts
		    WHERE user_id=$1 AND ($2::bigint=0 OR bank_id=$2) AND result != -1
		      AND fingerprint NOT IN (
		          SELECT fingerprint FROM sm2
		          WHERE user_id=$1 AND ($2::bigint=0 OR bank_id=$2)
		      )
		) sub`, userID, int64(bankID), today).Scan(&due)
	st.DueToday = int(due)
	return st
}

func (s *Store) GetUnitStats(ctx context.Context, userID string, bankID int) []store.UnitStat {
	if userID == "" {
		userID = "_legacy"
	}
	rows, err := s.pool.Query(ctx, `
		SELECT unit,
		       COUNT(*) AS total,
		       SUM(CASE WHEN result=1 THEN 1 ELSE 0 END) AS correct,
		       SUM(CASE WHEN result=0 THEN 1 ELSE 0 END) AS wrong
		FROM attempts WHERE user_id=$1
				AND ($2::bigint = 0 OR bank_id=$2) AND result != -1
				AND unit IS NOT NULL AND unit!=''
				GROUP BY unit ORDER BY total DESC LIMIT 30`, userID, int64(bankID))
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []store.UnitStat
	for rows.Next() {
		var unit string
		var total, correct, wrong int64
		if err := rows.Scan(&unit, &total, &correct, &wrong); err != nil {
			logger.Errorf("[pgstore] GetUnitStats scan error: %v", err)
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
		logger.Errorf("[pgstore] GetUnitStats rows error: %v", err)
	}
	return out
}

func (s *Store) GetWrongFingerprints(ctx context.Context, userID string, bankID int, limit int) []store.WrongEntry {
	if userID == "" {
		userID = "_legacy"
	}
	if limit <= 0 {
		limit = 300
	}
	// bankID=0: legacy/unscoped (mqb+PG混合模式) — 不过滤 bank_id，向后兼容
	var sql string
	var args []any
	if bankID == 0 {
		sql = `SELECT fingerprint,
		       COUNT(*) AS total,
		       SUM(CASE WHEN result=1 THEN 1 ELSE 0 END) AS correct,
		       SUM(CASE WHEN result=0 THEN 1 ELSE 0 END) AS wrong
		FROM attempts WHERE user_id=$1 AND result != -1
		GROUP BY fingerprint HAVING SUM(CASE WHEN result=0 THEN 1 ELSE 0 END)>0 AND SUM(CASE WHEN result=1 THEN 1 ELSE 0 END)*1.0/COUNT(*) < 0.8
		ORDER BY wrong DESC, MAX(ts) DESC LIMIT $2`
		args = []any{userID, limit}
	} else {
		sql = `SELECT fingerprint,
		       COUNT(*) AS total,
		       SUM(CASE WHEN result=1 THEN 1 ELSE 0 END) AS correct,
		       SUM(CASE WHEN result=0 THEN 1 ELSE 0 END) AS wrong
		FROM attempts WHERE user_id=$1 AND bank_id=$2 AND result != -1
		GROUP BY fingerprint HAVING SUM(CASE WHEN result=0 THEN 1 ELSE 0 END)>0 AND SUM(CASE WHEN result=1 THEN 1 ELSE 0 END)*1.0/COUNT(*) < 0.8
		ORDER BY wrong DESC, MAX(ts) DESC LIMIT $3`
		args = []any{userID, bankID, limit}
	}
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []store.WrongEntry
	for rows.Next() {
		var fp string
		var total, correct, wrong int64
		if err := rows.Scan(&fp, &total, &correct, &wrong); err != nil {
			logger.Errorf("[pgstore] GetWrongFingerprints scan error: %v", err)
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
		logger.Errorf("[pgstore] GetWrongFingerprints rows error: %v", err)
	}
	return out
}

func (s *Store) GetSyncStatus(ctx context.Context, userID string, bankID int) map[string]any {
	if userID == "" {
		userID = "_legacy"
	}
	var cnt int
	var lastTS *int64
	s.pool.QueryRow(ctx,
		`SELECT COUNT(*), MAX(ts) FROM sessions WHERE user_id=$1 AND ($2::bigint=0 OR bank_id=$2)`, userID, int64(bankID)).Scan(&cnt, &lastTS)
	return map[string]any{"session_count": cnt, "last_ts": lastTS}
}

func (s *Store) ClearUserData(ctx context.Context, userID string, bankID int) map[string]int {
	if userID == "" {
		userID = "_legacy"
	}
	// 显式写出每张表的 DELETE，避免 fmt.Sprintf 拼接表名（防御纵深）
	uid, bid := userID, int64(bankID)
	rA, _ := s.pool.Exec(ctx, "DELETE FROM attempts  WHERE user_id=$1 AND bank_id=$2", uid, bid)
	rS, _ := s.pool.Exec(ctx, "DELETE FROM sessions  WHERE user_id=$1 AND bank_id=$2", uid, bid)
	rM, _ := s.pool.Exec(ctx, "DELETE FROM sm2       WHERE user_id=$1 AND bank_id=$2", uid, bid)
	rF, _ := s.pool.Exec(ctx, "DELETE FROM favorites WHERE user_id=$1 AND bank_id=$2", uid, bid)
	return map[string]int{
		"attempts":  int(rA.RowsAffected()),
		"sessions":  int(rS.RowsAffected()),
		"sm2_cards": int(rM.RowsAffected()),
		"favorites": int(rF.RowsAffected()),
	}
}

// SyncFavorites applies adds/removes then returns the full current list for the user/bank.
// adds:    items to upsert (fingerprint+si+added_at)
// removes: items to delete (fingerprint+si pairs)

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
	// delete source — 显式列出每张表，不用 fmt.Sprintf 拼表名
	tx.Exec(ctx, "DELETE FROM attempts WHERE user_id=$1", fromUID)
	tx.Exec(ctx, "DELETE FROM sessions WHERE user_id=$1", fromUID)
	tx.Exec(ctx, "DELETE FROM sm2      WHERE user_id=$1", fromUID)
	return counts, tx.Commit(ctx)
}

// ── helpers ────────────────────────────────────────────────────────

func (s *Store) SaveAIChatLog(ctx context.Context, userID string, bankID int64,
	fp string, sqIdx int, userAnswer string,
	prompt []ai.ChatMessage, history []ai.ChatMessage,
	response string, reasoning string, truncated bool,
	model string, provider string) error {

	promptJSON, _ := json.Marshal(prompt)
	historyJSON, _ := json.Marshal(history)

	_, err := s.pool.Exec(ctx, `
		INSERT INTO ai_chat_logs(user_id,bank_id,fingerprint,sq_index,user_answer,
			prompt,history_in,response,reasoning,truncated,model,provider)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		userID, bankID, fp, sqIdx, userAnswer,
		string(promptJSON), string(historyJSON),
		response, reasoning, truncated, model, provider)
	if err != nil {
		logger.Errorf("[pgstore] SaveAIChatLog failed: %v", err)
	}
	return err
}

// CleanupAIChatLogs deletes logs older than retentionDays. Returns deleted count.

func (s *Store) CleanupAIChatLogs(ctx context.Context, retentionDays int) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM ai_chat_logs WHERE created_at < now() - $1 * interval '1 day'`,
		retentionDays)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// ── Exam Sessions ───────────────────────────────────────────────────────────

// SaveExamSession upserts an exam session row (used by server/exam_persist.go).

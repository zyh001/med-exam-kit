package progress

import (
	"database/sql"
	"encoding/json"
	"math"
	"strings"
	"time"
)

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
	for _, fp := range fps {
		dueArgs = append(dueArgs, fp)
	}
	dueArgs = append(dueArgs, userID)
	for _, fp := range fps {
		dueArgs = append(dueArgs, fp)
	}
	dueArgs = append(dueArgs, userID)
	for _, fp := range fps {
		dueArgs = append(dueArgs, fp)
	}
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

// CleanupStaleUsers deletes all data for users whose last activity
// was more than maxAgeDays ago. The _legacy user is never deleted.
// Returns the number of users cleaned up and total rows deleted.

package progress

import (
	"database/sql"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/zyh001/med-exam-kit/internal/ai"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := InitDB(filepath.Join(dir, "test.progress.db"))
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func makeSession(id, user string, correct, wrong int, fps []string) map[string]any {
	items := make([]any, len(fps))
	for i, fp := range fps {
		res := float64(1)
		if i >= correct {
			res = 0
		}
		items[i] = map[string]any{"fingerprint": fp, "result": res}
	}
	return map[string]any{
		"id": id, "mode": "A1型题",
		"total":    float64(len(fps)),
		"correct":  float64(correct),
		"wrong":    float64(wrong),
		"skip":     float64(0),
		"time_sec": float64(30),
		"items":    items,
	}
}

func TestRecordSession_Basic(t *testing.T) {
	db := openTestDB(t)
	if err := RecordSession(db, makeSession("s1", "u1", 4, 1, []string{"fp1", "fp2", "fp3", "fp4", "fp5"}), "u1"); err != nil {
		t.Fatalf("RecordSession: %v", err)
	}
	h := GetHistory(db, "u1", 10)
	if len(h) != 1 || h[0].Correct != 4 {
		t.Fatalf("want 1 history with correct=4, got %+v", h)
	}
}

func TestRecordSession_UsersIsolated(t *testing.T) {
	db := openTestDB(t)
	RecordSession(db, makeSession("sa", "alice", 1, 0, []string{"fpa"}), "alice")
	RecordSession(db, makeSession("sb", "bob", 1, 0, []string{"fpb"}), "bob")
	if h := GetHistory(db, "alice", 10); len(h) != 1 {
		t.Fatalf("alice should have 1 session, got %d", len(h))
	}
	if h := GetHistory(db, "bob", 10); len(h) != 1 {
		t.Fatalf("bob should have 1 session, got %d", len(h))
	}
}

func TestSM2_FirstCorrect_DueToday(t *testing.T) {
	db := openTestDB(t)
	RecordSession(db, makeSession("s1", "u1", 1, 0, []string{"fp-sm2"}), "u1")
	fps := GetDueFingerprints(db, "u1")
	found := false
	for _, fp := range fps {
		if fp == "fp-sm2" {
			found = true
		}
	}
	// 修复后：第一次答对 interval=1（明天复习），今天不应出现在待复习列表
	if found {
		t.Fatal("fp-sm2 should NOT be due today after first correct answer (interval=1 → due tomorrow)")
	}
	// 验证 SM2 记录确实写入且 interval=1
	var interval, reps int
	db.QueryRow("SELECT interval, reps FROM sm2 WHERE user_id='u1' AND fingerprint='fp-sm2'").
		Scan(&interval, &reps)
	if interval != 1 || reps != 1 {
		t.Fatalf("first correct: want interval=1 reps=1, got interval=%d reps=%d", interval, reps)
	}
}

func TestSM2_WrongAnswerResets(t *testing.T) {
	db := openTestDB(t)
	RecordSession(db, makeSession("s1", "u1", 1, 0, []string{"fp1"}), "u1")
	// Now wrong
	wrongSess := map[string]any{
		"id": "s2", "mode": "A1", "total": float64(1),
		"correct": float64(0), "wrong": float64(1), "skip": float64(0), "time_sec": float64(5),
		"items": []any{map[string]any{"fingerprint": "fp1", "result": float64(0)}},
	}
	RecordSession(db, wrongSess, "u1")

	var interval, reps int
	db.QueryRow("SELECT interval, reps FROM sm2 WHERE user_id='u1' AND fingerprint='fp1'").
		Scan(&interval, &reps)
	// 修复后：答错 reps=0 且 interval=1（明天再复习，不再是当天重复）
	if reps != 0 || interval != 1 {
		t.Fatalf("wrong answer should reset: interval=%d reps=%d (want interval=1 reps=0)", interval, reps)
	}
}

func TestSM2_EFFloor(t *testing.T) {
	db := openTestDB(t)
	for i := 0; i < 20; i++ {
		id := "sw" + string(rune('a'+i%26))
		sess := map[string]any{
			"id": id, "mode": "A1", "total": float64(1),
			"correct": float64(0), "wrong": float64(1), "skip": float64(0), "time_sec": float64(5),
			"items": []any{map[string]any{"fingerprint": "fpe", "result": float64(0)}},
		}
		RecordSession(db, sess, "u2")
	}
	var ef float64
	db.QueryRow("SELECT ef FROM sm2 WHERE user_id='u2' AND fingerprint='fpe'").Scan(&ef)
	if ef < minEF-0.001 {
		t.Fatalf("EF dropped below floor: %.4f < %.2f", ef, minEF)
	}
	if math.Abs(ef-minEF) > 0.001 {
		t.Fatalf("EF should be at floor after many wrong answers: want %.2f, got %.4f", minEF, ef)
	}
}

func TestGetOverallStats(t *testing.T) {
	db := openTestDB(t)
	RecordSession(db, makeSession("os1", "u3", 2, 1, []string{"fp1", "fp2", "fp3"}), "u3")
	s := GetOverallStats(db, "u3")
	if s.TotalAttempts != 3 {
		t.Fatalf("want 3, got %d", s.TotalAttempts)
	}
	if s.Accuracy != 67 {
		t.Fatalf("want 67%%, got %d", s.Accuracy)
	}
	if s.Sessions != 1 {
		t.Fatalf("want 1 session, got %d", s.Sessions)
	}
}

func TestClearUserData(t *testing.T) {
	db := openTestDB(t)
	RecordSession(db, makeSession("cl1", "uc", 1, 0, []string{"fp1"}), "uc")
	counts := ClearUserData(db, "uc")
	if counts["sessions"] != 1 {
		t.Fatalf("want 1 session deleted, got %d", counts["sessions"])
	}
	if h := GetHistory(db, "uc", 10); len(h) != 0 {
		t.Fatal("history should be empty after clear")
	}
}

func TestRecordSessionsBatch_SkipsDuplicates(t *testing.T) {
	db := openTestDB(t)
	sessions := []map[string]any{
		makeSession("b1", "u4", 1, 0, []string{"fp1"}),
		makeSession("b2", "u4", 1, 0, []string{"fp2"}),
	}
	done, skipped := RecordSessionsBatch(db, sessions, "u4")
	if len(done) != 2 || len(skipped) != 0 {
		t.Fatalf("first batch: done=%d skipped=%d", len(done), len(skipped))
	}
	// Repeat — same sessions should be skipped
	done2, skipped2 := RecordSessionsBatch(db, sessions, "u4")
	if len(done2) != 0 || len(skipped2) != 2 {
		t.Fatalf("second batch: done=%d skipped=%d", len(done2), len(skipped2))
	}
}

func TestDBPathForBank(t *testing.T) {
	got := DBPathForBank("/data/output/bank.mqb")
	want := "/data/output/bank.progress.db"
	if got != want {
		t.Fatalf("want %s, got %s", want, got)
	}
}

func TestSaveAIChatLog_Basic(t *testing.T) {
	db := openTestDB(t)

	prompt := []ai.ChatMessage{
		{Role: "system", Content: "你是医学考试辅导专家"},
		{Role: "user", Content: "题干: 患者出现..."},
	}
	history := []ai.ChatMessage{
		{Role: "user", Content: "为什么选B？"},
	}

	err := SaveAIChatLog(db, "u1", "fp123", 0, "A", prompt, history,
		"本题考查...", "推理过程...", false, "deepseek-chat", "deepseek")
	if err != nil {
		t.Fatalf("SaveAIChatLog: %v", err)
	}

	// Verify the row was inserted
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM ai_chat_logs WHERE user_id='u1' AND fingerprint='fp123'").Scan(&count)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 1 {
		t.Fatalf("want 1 row, got %d", count)
	}

	// Verify fields
	var userID, fp, response, reasoning, model, provider string
	var sqIdx, truncated int
	err = db.QueryRow("SELECT user_id, fingerprint, sq_index, response, reasoning, truncated, model, provider FROM ai_chat_logs WHERE user_id='u1'").Scan(
		&userID, &fp, &sqIdx, &response, &reasoning, &truncated, &model, &provider)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if userID != "u1" || fp != "fp123" || sqIdx != 0 || response != "本题考查..." || model != "deepseek-chat" {
		t.Fatalf("fields mismatch: uid=%s fp=%s sqIdx=%d response=%s model=%s", userID, fp, sqIdx, response, model)
	}
	if truncated != 0 {
		t.Fatalf("want truncated=0, got %d", truncated)
	}
}

func TestSaveAIChatLog_Truncated(t *testing.T) {
	db := openTestDB(t)

	err := SaveAIChatLog(db, "u2", "fp456", 1, "C", nil, nil,
		"部分回复...", "", true, "gpt-4o", "openai")
	if err != nil {
		t.Fatalf("SaveAIChatLog: %v", err)
	}

	var truncated int
	err = db.QueryRow("SELECT truncated FROM ai_chat_logs WHERE user_id='u2'").Scan(&truncated)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if truncated != 1 {
		t.Fatalf("want truncated=1, got %d", truncated)
	}
}

func TestCleanupAIChatLogs(t *testing.T) {
	db := openTestDB(t)

	// Insert a "current" log
	err := SaveAIChatLog(db, "u3", "fp_cur", 0, "A", nil, nil,
		"current", "", false, "model", "provider")
	if err != nil {
		t.Fatalf("SaveAIChatLog: %v", err)
	}

	// Insert an "old" log by manually setting created_at to 31 days ago
	cutoff := time.Now().Add(-31 * 24 * time.Hour).UnixMilli()
	_, err = db.Exec(`INSERT INTO ai_chat_logs(user_id,fingerprint,sq_index,user_answer,prompt,history_in,response,reasoning,truncated,model,provider,created_at)
		VALUES('u3','fp_old',0,'B','[]','[]','old','',0,'model','provider',?)`, cutoff)
	if err != nil {
		t.Fatalf("insert old log: %v", err)
	}

	// Cleanup with 30-day retention
	n, err := CleanupAIChatLogs(db, 30)
	if err != nil {
		t.Fatalf("CleanupAIChatLogs: %v", err)
	}
	if n != 1 {
		t.Fatalf("want 1 deleted, got %d", n)
	}

	// Verify old row removed, current row remains
	var count int
	db.QueryRow("SELECT COUNT(*) FROM ai_chat_logs WHERE user_id='u3'").Scan(&count)
	if count != 1 {
		t.Fatalf("want 1 remaining row, got %d", count)
	}
}

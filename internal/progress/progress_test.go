package progress

import (
	"database/sql"
	"math"
	"path/filepath"
	"testing"
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
	if !found {
		t.Fatal("fp-sm2 should be due today after first correct answer (interval=0 → due today)")
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
	if reps != 0 || interval != 0 {
		t.Fatalf("wrong answer should reset: interval=%d reps=%d (want interval=0 reps=0)", interval, reps)
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

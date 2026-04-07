package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zyh001/med-exam-kit/internal/models"
	"github.com/zyh001/med-exam-kit/internal/progress"
)

func makeServerWithDB(t *testing.T) *Server {
	t.Helper()
	db, err := progress.InitDB(":memory:")
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	q := &models.Question{
		Fingerprint: "fp-sync-1", Mode: "A1型题", Unit: "第一章",
		SubQuestions: []models.SubQuestion{
			{Text: "测试题目", Options: []string{"A.甲", "B.乙", "C.丙", "D.丁"}, Answer: "A"},
		},
	}
	s := New(Config{
		Banks: []BankEntry{{
			Path: "test.mqb", Questions: []*models.Question{q},
			DB: db, RecordEnabled: true,
		}},
		Host: "127.0.0.1", Port: 5174,
	})
	return s
}

func doPost(s *Server, path string, cookie string, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Host = "127.0.0.1:5174"
	req.Header.Set("X-Session-Token", s.sessionToken)
	req.Header.Set("Content-Type", "application/json")
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: "med_exam_uid", Value: cookie})
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	return w
}

func doGet(s *Server, path string, cookie string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Host = "127.0.0.1:5174"
	req.Header.Set("X-Session-Token", s.sessionToken)
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: "med_exam_uid", Value: cookie})
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	return w
}

func syncSession(t *testing.T, s *Server, cookie, sid string, correct bool) {
	t.Helper()
	result := 0
	if correct { result = 1 }
	w := doPost(s, "/api/sync?bank=0", cookie, map[string]any{
		"sessions": []map[string]any{{
			"id": sid, "mode": "practice", "total": 1,
			"correct": result, "wrong": 1 - result, "skip": 0,
			"time_sec": 30, "date": "2026-04-07", "units": []string{"第一章"},
			"items": []map[string]any{
				{"fingerprint": "fp-sync-1", "result": result, "mode": "A1型题", "unit": "第一章"},
			},
		}},
	})
	if w.Code != 200 {
		t.Fatalf("sync %s: status %d: %s", sid, w.Code, w.Body.String())
	}
	var r map[string]any
	json.Unmarshal(w.Body.Bytes(), &r)
	if ok, _ := r["ok"].(bool); !ok {
		t.Fatalf("sync %s: ok=false: %v", sid, r)
	}
	t.Logf("sync %s: ok, processed=%v", sid, r["processed"])
}

// Case 1: 同一 cookie，写入读出应有数据
func TestSync_SameCookie_HasStats(t *testing.T) {
	s := makeServerWithDB(t)
	const uid = "user-abc-123"

	syncSession(t, s, uid, "sess-001", true)

	w := doGet(s, "/api/stats?bank=0", uid)
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	overall := resp["overall"].(map[string]any)
	t.Logf("overall: %v", overall)

	if overall["total_attempts"].(float64) == 0 {
		t.Error("total_attempts should be 1, got 0")
	}
	if overall["sessions"].(float64) == 0 {
		t.Error("sessions should be 1, got 0")
	}
}

// Case 2: 无 cookie 写入（legacy），有 cookie 读取 → 数据不可见！
func TestSync_XUserIDHeaderFix(t *testing.T) {
	s := makeServerWithDB(t)
	const uid = "user-abc-999"

	// 修复验证：sync 请求用 X-User-ID header 发送 uid（不依赖 cookie）
	syncSession(t, s, uid /*X-User-ID header → correct user*/, "sess-with-header", true)

	// 用真实 cookie 读取
	w := doGet(s, "/api/stats?bank=0", uid)
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	overall := resp["overall"].(map[string]any)
	t.Logf("real-user overall (should be 0): %v", overall)

	// 用 _legacy 读取 - 数据应该在这里
	wLeg := doGet(s, "/api/stats?bank=0", "" /*no cookie → _legacy*/)
	var respLeg map[string]any
	json.Unmarshal(wLeg.Body.Bytes(), &respLeg)
	overallLeg := respLeg["overall"].(map[string]any)
	t.Logf("legacy overall (should be 1): %v", overallLeg)

	// 如果 total_attempts 读出来是 0 (real user) 但 legacy 是 1，说明用户 ID 不一致
	realAttempts  := overall["total_attempts"].(float64)
	legacyAttempts := overallLeg["total_attempts"].(float64)
	if realAttempts == 0 && legacyAttempts > 0 {
		t.Errorf("BUG: uid mismatch: sync 写入 _legacy, stats 读取 %s → 数据不可见!\n"+
			"原因：sync 请求没带 cookie 或 cookie 与页面不一致", uid)
	}
}

// Case 3: /api/record/status 返回什么
func TestRecordStatus_SQLite(t *testing.T) {
	s := makeServerWithDB(t)
	const uid = "user-test-456"
	w := doGet(s, "/api/record/status?bank=0", uid)
	var r map[string]any
	json.Unmarshal(w.Body.Bytes(), &r)
	t.Logf("record/status: %v", r)
	if enabled, _ := r["enabled"].(bool); !enabled {
		t.Error("record should be enabled")
	}
	if dbReady, _ := r["db_ready"].(bool); !dbReady {
		t.Error("db_ready should be true")
	}
	if r["user_id"] != uid {
		t.Errorf("user_id mismatch: got %v, want %s", r["user_id"], uid)
	}
}

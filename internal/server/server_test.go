package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zyh001/med-exam-kit/internal/models"
)

func makeServer() *Server {
	questions := []*models.Question{
		{
			Fingerprint: "fp001",
			Mode:        "A1型题",
			Unit:        "第一章",
			SubQuestions: []models.SubQuestion{
				{Text: "题目1", Options: []string{"A.甲", "B.乙", "C.丙"}, Answer: "A"},
			},
		},
		{
			Fingerprint: "fp002",
			Mode:        "A2型题",
			Unit:        "第二章",
			SubQuestions: []models.SubQuestion{
				{Text: "题目2", Options: []string{"A.甲", "B.乙", "C.丙", "D.丁"}, Answer: "B"},
				{Text: "题目2b", Options: []string{"A.甲", "B.乙", "C.丙", "D.丁"}, Answer: "C"},
			},
		},
		{
			Fingerprint: "fp003",
			Mode:        "B1型题",
			Unit:        "第三章",
			SubQuestions: []models.SubQuestion{
				{Text: "题目3", Options: []string{"A.甲", "B.乙", "C.丙"}, Answer: "C"},
			},
		},
	}
	return New(Config{
		Questions:     questions,
		Host:          "127.0.0.1",
		Port:          5174,
		RecordEnabled: false,
	})
}

func apiGet(s *Server, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path, nil)
	req.Host = "127.0.0.1:5174"
	req.Header.Set("X-Session-Token", s.sessionToken)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	return w
}

func TestHandleInfo(t *testing.T) {
	s := makeServer()
	w := apiGet(s, "/api/info")
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	if v, ok := resp["total_sq"].(float64); !ok || int(v) != 4 {
		t.Fatalf("wrong total_sq: %v", resp["total_sq"])
	}
}

func TestHandleQuestions_All(t *testing.T) {
	s := makeServer()
	w := apiGet(s, "/api/questions")
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	total := int(resp["total"].(float64))
	if total != 4 { // 1 + 2 + 1 sub-questions
		t.Fatalf("want 4 sub-questions total, got %d", total)
	}
}

func TestHandleQuestions_ModeFilter(t *testing.T) {
	s := makeServer()
	w := apiGet(s, "/api/questions?mode=A1型题")
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	total := int(resp["total"].(float64))
	if total != 1 {
		t.Fatalf("A1 filter: want 1, got %d", total)
	}
}

func TestHandleQuestions_Limit_NoOvershoot(t *testing.T) {
	s := makeServer()
	// limit=1: the A2 question has 2 sub-questions, should be skipped
	// only the A1 (1 sub-q) should fit
	w := apiGet(s, "/api/questions?limit=1")
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	total := int(resp["total"].(float64))
	if total > 1 {
		t.Fatalf("limit=1 must never return more than 1 sub-question, got %d", total)
	}
}

func TestHandleQuestions_Fingerprints(t *testing.T) {
	s := makeServer()
	w := apiGet(s, "/api/questions?fingerprints=fp001,fp003")
	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	items := resp["items"].([]any)
	for _, item := range items {
		m := item.(map[string]any)
		fp := m["fingerprint"].(string)
		if fp != "fp001" && fp != "fp003" {
			t.Fatalf("unexpected fingerprint %s in result", fp)
		}
	}
}

func TestMissingSessionToken_Returns401(t *testing.T) {
	s := makeServer()
	req := httptest.NewRequest("GET", "/api/info", nil)
	req.Host = "127.0.0.1:5174"
	// No X-Session-Token header
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
}

func TestDNSRebinding_WrongHostReturns403(t *testing.T) {
	s := makeServer()
	req := httptest.NewRequest("GET", "/api/info", nil)
	req.Host = "evil.com"
	req.Header.Set("X-Session-Token", s.sessionToken)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403 for wrong host, got %d", w.Code)
	}
}

func TestPINGate_RedirectsToLoginPage(t *testing.T) {
	s := New(Config{
		Questions:  []*models.Question{},
		Host:       "127.0.0.1",
		Port:       5174,
		AccessCode: "TESTCODE",
	})
	// Use exact root "/" which matches the /{$} pattern
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "127.0.0.1:5174"
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 (PIN page), got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "访问码") {
		t.Fatal("response should contain PIN page")
	}
}

func TestGreedyFill_NeverExceedsTarget(t *testing.T) {
	pool := []group{
		{{ID: "1"}, {ID: "2"}, {ID: "3"}}, // 3 sub-questions
		{{ID: "4"}},                       // 1
		{{ID: "5"}, {ID: "6"}},            // 2
	}
	result := greedyFill(pool, 2)
	total := 0
	for _, g := range result {
		total += len(g)
	}
	if total > 2 {
		t.Fatalf("greedyFill exceeded target: got %d > 2", total)
	}
}

func TestGreedyFill_FillsExactly(t *testing.T) {
	pool := []group{
		{{ID: "1"}},
		{{ID: "2"}},
		{{ID: "3"}},
	}
	result := greedyFill(pool, 2)
	total := 0
	for _, g := range result {
		total += len(g)
	}
	if total != 2 {
		t.Fatalf("greedyFill should reach exactly 2, got %d", total)
	}
}

package server

import (
	"net/http"
	"sync"

	"github.com/zyh001/med-exam-kit/internal/logger"
)

// ReloadFunc is injected by cmd/quiz.go.  It reads the config / loads banks
// and returns the new bank list.  banks/password are optional overrides;
// when empty the function should re-read the config file.
type ReloadFunc func(banks []string, password string) ([]BankEntry, error)

var banksMu sync.RWMutex // protects s.cfg.Banks during hot-reload

// swapBanks atomically replaces the bank list and closes orphaned SQLite DBs.
func (s *Server) swapBanks(next []BankEntry) {
	banksMu.Lock()
	old := s.cfg.Banks
	s.cfg.Banks = next
	banksMu.Unlock()

	// Close DB connections that are no longer used.
	keep := make(map[string]bool, len(next))
	for _, b := range next {
		keep[b.Path] = true
	}
	for _, b := range old {
		if !keep[b.Path] && b.DB != nil {
			if err := b.DB.Close(); err != nil {
				logger.Warnf("[reload] 关闭旧 DB 失败 (%s): %v", b.Path, err)
			}
		}
	}
	logger.Infof("[reload] 题库已替换，共 %d 个", len(next))
}

// HotReload performs a live reload using the registered ReloadFunc.
// Safe to call from a goroutine (e.g. SIGHUP handler).
func (s *Server) HotReload(bankOverride []string, passwordOverride string) error {
	if s.reloadFn == nil {
		return nil
	}
	next, err := s.reloadFn(bankOverride, passwordOverride)
	if err != nil {
		return err
	}
	s.swapBanks(next)
	return nil
}

// handleAdminReload handles POST /api/admin/reload
//
//	Body (JSON, all optional):
//	  {"banks": ["exam.mqb", "pg:bank:2"], "password": "secret"}
//
// Empty body → re-reads the YAML config file (via reloadFn).
func (s *Server) handleAdminReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if s.reloadFn == nil {
		jsonError(w, "热重载未启用", http.StatusNotImplemented)
		return
	}

	type reloadReq struct {
		Banks    []string `json:"banks"`
		Password string   `json:"password"`
	}
	var req reloadReq
	if r.ContentLength > 0 {
		decodeJSONBody(w, r, &req)
	}

	next, err := s.reloadFn(req.Banks, req.Password)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.swapBanks(next)

	type bi struct {
		Path  string `json:"path"`
		Name  string `json:"name"`
		Total int    `json:"total"`
	}
	infos := make([]bi, len(next))
	for i, b := range next {
		infos[i] = bi{Path: b.Path, Name: b.bankName(), Total: len(b.Questions)}
	}
	jsonOK(w, map[string]any{"ok": true, "banks": infos})
}

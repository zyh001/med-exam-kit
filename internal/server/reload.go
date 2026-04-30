package server

import (
	"net/http"
	"sync"

	"github.com/zyh001/med-exam-kit/internal/ai"
	"github.com/zyh001/med-exam-kit/internal/logger"
)

// ReloadedConfig carries the mutable runtime configuration fields that
// may change between hot-reloads (everything except Host/Port/Assets).
type ReloadedConfig struct {
	AccessCode   string
	CookieSecret string
	PinLen       int
	AIProvider       string
	AIModel          string
	AIAPIKey         string
	AIBaseURL        string
	AIEnableThinking *bool
	AIMaxTokens      int
	ASRAPIKey  string
	ASRModel   string
	ASRBaseURL string
	S3Endpoint   string
	S3Bucket     string
	S3AccessKey  string
	S3SecretKey  string
	S3PublicBase string
	CleanupDays            int
	AIChatLogRetentionDays int
	Debug                  bool
	TrustedProxies         []string
}

// ReloadFunc is injected by cmd/quiz.go.  It reads the config / loads banks
// and returns the new bank list plus updated config.  banks/password are
// optional overrides; when empty the function should re-read the config file.
type ReloadFunc func(banks []string, password string) ([]BankEntry, *ReloadedConfig, error)

var banksMu sync.RWMutex // protects s.cfg.Banks and mutable config fields during hot-reload

// applyReload atomically replaces the bank list, updates mutable config fields,
// and recreates the AI client if AI settings changed. Closes orphaned SQLite DBs.
func (s *Server) applyReload(next []BankEntry, rc *ReloadedConfig) {
	banksMu.Lock()
	old := s.cfg.Banks
	s.cfg.Banks = next

	// Apply mutable config if provided
	if rc != nil {
		s.cfg.AccessCode = rc.AccessCode
		s.cfg.CookieSecret = rc.CookieSecret
		s.cfg.PinLen = rc.PinLen
		s.cfg.AIProvider = rc.AIProvider
		s.cfg.AIModel = rc.AIModel
		s.cfg.AIAPIKey = rc.AIAPIKey
		s.cfg.AIBaseURL = rc.AIBaseURL
		s.cfg.AIEnableThinking = rc.AIEnableThinking
		s.cfg.AIMaxTokens = rc.AIMaxTokens
		s.cfg.ASRAPIKey = rc.ASRAPIKey
		s.cfg.ASRModel = rc.ASRModel
		s.cfg.ASRBaseURL = rc.ASRBaseURL
		s.cfg.S3Endpoint = rc.S3Endpoint
		s.cfg.S3Bucket = rc.S3Bucket
		s.cfg.S3AccessKey = rc.S3AccessKey
		s.cfg.S3SecretKey = rc.S3SecretKey
		s.cfg.S3PublicBase = rc.S3PublicBase
		s.cfg.CleanupDays = rc.CleanupDays
		s.cfg.AIChatLogRetentionDays = rc.AIChatLogRetentionDays
		s.cfg.Debug = rc.Debug
		s.cfg.TrustedProxies = rc.TrustedProxies
	}
	banksMu.Unlock()

	// Recreate AI client if config changed (or was newly added/removed)
	if rc != nil {
		if rc.AIAPIKey != "" {
			newClient := ai.NewClient(rc.AIProvider, rc.AIAPIKey, rc.AIBaseURL, rc.AIModel, 120, rc.AIEnableThinking)
			s.aiClient = newClient
			logger.Infof("[reload] AI 客户端已重建: provider=%s model=%s", rc.AIProvider, newClient.Model)
		} else {
			s.aiClient = nil
			logger.Infof("[reload] AI 已禁用（未配置 api_key）")
		}
	}

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
	logger.Infof("[reload] 热重载完成，题库 %d 个", len(next))
}

// HotReload performs a live reload using the registered ReloadFunc.
// Safe to call from a goroutine (e.g. SIGHUP handler).
func (s *Server) HotReload(bankOverride []string, passwordOverride string) error {
	if s.reloadFn == nil {
		return nil
	}
	next, rc, err := s.reloadFn(bankOverride, passwordOverride)
	if err != nil {
		return err
	}
	s.applyReload(next, rc)
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

	next, rc, err := s.reloadFn(req.Banks, req.Password)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.applyReload(next, rc)

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

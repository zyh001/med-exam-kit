package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strings"

	"github.com/zyh001/med-exam-kit/internal/auth"
)

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if _, err := r.Cookie("med_exam_uid"); err != nil {
		uid := make([]byte, 16)
		rand.Read(uid)
		http.SetCookie(w, &http.Cookie{
			Name: "med_exam_uid", Value: hex.EncodeToString(uid),
			MaxAge: 365 * 24 * 3600, HttpOnly: false,
			SameSite: http.SameSiteLaxMode, Path: "/",
		})
	}
	if s.cfg.Assets != nil {
		data, err := fs.ReadFile(s.cfg.Assets, "assets/templates/quiz.html")
		if err == nil {
			html := strings.Replace(string(data), "{{SESSION_TOKEN}}", s.sessionToken, 1)
			html = strings.Replace(html, "{{ASSET_VER}}", s.assetVer, 1)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			io.WriteString(w, html)
			return
		}
	}
	http.Error(w, "quiz.html not found — embed assets first", http.StatusNotFound)
}

func (s *Server) handleEditor(w http.ResponseWriter, r *http.Request) {
	if _, err := r.Cookie("med_exam_uid"); err != nil {
		uid := make([]byte, 16)
		rand.Read(uid)
		http.SetCookie(w, &http.Cookie{
			Name: "med_exam_uid", Value: hex.EncodeToString(uid),
			MaxAge: 365 * 24 * 3600, HttpOnly: false,
			SameSite: http.SameSiteLaxMode, Path: "/",
		})
	}
	if s.cfg.Assets != nil {
		data, err := fs.ReadFile(s.cfg.Assets, "assets/templates/editor.html")
		if err == nil {
			html := strings.Replace(string(data), "{{SESSION_TOKEN}}", s.sessionToken, 1)
			html = strings.Replace(html, "{{ASSET_VER}}", s.assetVer, 1)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			io.WriteString(w, html)
			return
		}
	}
	http.Error(w, "editor.html not found — embed assets first", http.StatusNotFound)
}

func (s *Server) handleAuth(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	code := strings.TrimSpace(strings.ToUpper(r.FormValue("code")))
	ip := s.remoteIP(r)

	// 如果需要验证码，先校验
	if auth.NeedsCaptcha(ip) {
		token := r.FormValue("captcha_token")
		answer := r.FormValue("captcha_answer")
		if !auth.VerifyCaptcha(token, answer, ip) {
			// 验证码错误：重新生成并展示，不计入访问码失败次数
			tok, svg := auth.NewCaptcha()
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			io.WriteString(w, auth.RenderPINPage("医考练习", "验证码错误，请重新计算", s.cfg.PinLen, tok, svg))
			return
		}
	}

	// Session J 加固：访问码比较改为常数时间，防止逐字节时序攻击。
	// 空访问码（禁用 PIN 模式）保持之前的直接放行语义。
	if s.cfg.AccessCode == "" ||
		subtle.ConstantTimeCompare([]byte(code), []byte(s.cfg.AccessCode)) == 1 {
		auth.RecordSuccess(ip)
		auth.SetAuthCookie(w, r, s.cfg.CookieSecret, s.cfg.AccessCode)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	auth.RecordFailure(ip)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	var tok, svg string
	if auth.NeedsCaptcha(ip) {
		tok, svg = auth.NewCaptcha()
	}
	io.WriteString(w, auth.RenderPINPage("医考练习", "访问码错误，请重试", s.cfg.PinLen, tok, svg))
}

// ── Health check ───────────────────────────────────────────────────────

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	status := "ok"
	dbStatus := make(map[string]string)
	for i, b := range s.cfg.Banks {
		if b.DB == nil {
			dbStatus[fmt.Sprintf("bank_%d", i)] = "not_configured"
			continue
		}
		if err := b.DB.Ping(); err != nil {
			dbStatus[fmt.Sprintf("bank_%d", i)] = "error"
			status = "degraded"
		} else {
			dbStatus[fmt.Sprintf("bank_%d", i)] = "ok"
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(map[string]any{
		"status":  status,
		"banks":   len(s.cfg.Banks),
		"db":      dbStatus,
		"version": s.assetVer,
	})
}

// ── API: banks list ───────────────────────────────────────────────────

// GET /api/banks  — returns metadata for all loaded banks

func (s *Server) handleBanks(w http.ResponseWriter, r *http.Request) {
	type bankInfo struct {
		ID    int    `json:"id"`
		Name  string `json:"name"`
		Path  string `json:"path"`
		Total int    `json:"total_sq"`
	}
	infos := make([]bankInfo, len(s.cfg.Banks))
	for i, b := range s.cfg.Banks {
		total := 0
		for _, q := range b.Questions {
			total += len(q.SubQuestions)
		}
		infos[i] = bankInfo{
			ID:    i,
			Name:  b.bankName(),
			Path:  b.Path,
			Total: total,
		}
	}
	jsonOK(w, map[string]any{
		"banks":         infos,
		"session_token": s.sessionToken,
		"asset_ver":     s.assetVer,
	})
}

// ── API: per-bank info ────────────────────────────────────────────────

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	b, _, ok := s.bankForReq(r)
	if !ok {
		jsonError(w, "bank not found", http.StatusNotFound)
		return
	}
	modeSet := []string{}
	modeMap := map[string]bool{}
	unitSet := []string{}
	unitMap := map[string]bool{}
	unitSq := map[string]int{}
	unitModeSq := map[string]map[string]int{}
	total := 0
	for _, q := range b.Questions {
		if !modeMap[q.Mode] {
			modeMap[q.Mode] = true
			modeSet = append(modeSet, q.Mode)
		}
		if !unitMap[q.Unit] {
			unitMap[q.Unit] = true
			unitSet = append(unitSet, q.Unit)
			unitSq[q.Unit] = 0
			unitModeSq[q.Unit] = map[string]int{}
		}
		cnt := len(q.SubQuestions)
		total += cnt
		unitSq[q.Unit] += cnt
		unitModeSq[q.Unit][q.Mode] += cnt
	}
	jsonOK(w, map[string]any{
		"bank_name":      b.bankName(),
		"total_sq":       total,
		"units":          unitSet,
		"modes":          modeSet,
		"unit_sq":        unitSq,
		"unit_mode_sq":   unitModeSq,
		"session_token":  s.sessionToken,
		"asset_ver":      s.assetVer,
		"record_enabled": b.RecordEnabled,
		"ai_enabled":     s.aiClient != nil,
		"asr_enabled":    s.cfg.ASRAPIKey != "",
		"s3_enabled":     s.cfg.S3Endpoint != "" && s.cfg.S3Bucket != "" && s.cfg.S3AccessKey != "",
	})
}

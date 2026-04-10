package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"math"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/zyh001/med-exam-kit/internal/auth"
	"github.com/zyh001/med-exam-kit/internal/bank"
	"github.com/zyh001/med-exam-kit/internal/models"
	"github.com/zyh001/med-exam-kit/internal/progress"
	"github.com/zyh001/med-exam-kit/internal/store"
)

// BankEntry holds one loaded question bank and its associated state.
type BankEntry struct {
	Path          string
	Name          string             // display name; overrides Path-derived name
	BankID        int                // PG bank_id (0 for SQLite/legacy)
	Password      string
	Questions     []*models.Question
	DB            *sql.DB             // SQLite progress DB (nil when using PgStore)
	PgStore       pgStorer            // PostgreSQL store (nil when using SQLite)
	RecordEnabled bool
}

// shareStorer is an optional interface that pgStorer implementations may satisfy
// to persist share tokens across server restarts (PG mode only).
// JSON []byte is used to bridge the type without creating an import cycle.
type shareStorer interface {
	SaveShareTokenJSON(ctx context.Context, token string, data []byte) error
	LoadShareTokenJSON(ctx context.Context, token string) []byte
	DeleteShareToken(ctx context.Context, token string)
	CleanExpiredShareTokens(ctx context.Context)
}
// allowing server.go to stay decoupled from the concrete pgstore type.
type pgStorer interface {
	DeleteSession(ctx context.Context, sessionID, userID string) bool
	RecordSessionsBatch(ctx context.Context, sessions []map[string]any, userID string) (processed, skipped []string)
	GetOverallStats(ctx context.Context, userID string, bankID int, clientDate string) store.OverallStats
	GetUnitStats(ctx context.Context, userID string, bankID int) []store.UnitStat
	GetWrongFingerprints(ctx context.Context, userID string, bankID int, limit int) []store.WrongEntry
	GetDueFingerprints(ctx context.Context, userID string, bankID int, clientDate string) []string
	GetHistory(ctx context.Context, userID string, bankID int, limit int) []store.HistoryEntry
	GetSyncStatus(ctx context.Context, userID string, bankID int) map[string]any
	ClearUserData(ctx context.Context, userID string, bankID int) map[string]int
	DiagAttempts(ctx context.Context, userID string, bankID int) map[string]any
	ImportBank(ctx context.Context, name, source string, questions []*models.Question) (int64, error)
}

// bankName derives a display name from the file path (or Name if set).
func (b *BankEntry) bankName() string {
	if b.Name != "" { return b.Name }
	base := filepath.Base(b.Path)
	ext := filepath.Ext(base)
	return strings.TrimSuffix(base, ext)
}

type Config struct {
	Banks        []BankEntry
	Assets       fs.FS
	Host         string
	Port         int
	AccessCode   string
	CookieSecret string
	PinLen       int

	// Legacy single-bank fields kept for editor command compatibility.
	// When Banks is set, these are ignored.
	Questions     []*models.Question
	DB            *sql.DB
	RecordEnabled bool
	BankPath      string
	Password      string
}

type Server struct {
	cfg          Config
	sessionToken string
	assetVer     string
	mux          *http.ServeMux
	rateMu       sync.Mutex
	rateBuckets  map[string][]time.Time
	httpServer   *http.Server
	// 图标 PNG 缓存（启动时按需生成一次，避免每次请求重复编码）
	iconOnce   sync.Once
	icon192    []byte
	icon512    []byte
	// Web Push
	vapidKeys      *VAPIDKeys
	pushStores     map[string]*pushStore // bankPath → store
	pushTestMu     sync.Mutex
	pushTestBuckets map[string][]time.Time // ip → timestamps for push/test rate limit
	// 考试防作弊：sealed 模式下答案存服务端，提交后才下发
	examMu       sync.Mutex
	examSessions map[string]*examSession // examID → answers
	// 试卷分享
	shareMu     sync.Mutex
	shareTokens map[string]*shareConfig
}

type examAnswer struct {
	Answer  string `json:"answer"`
	Discuss string `json:"discuss"`
}
type examSession struct {
	answers map[string]examAnswer // fingerprint → answer+discuss
	ts      int64
}

type shareConfig struct {
	Fingerprints   []string               `json:"fingerprints"`
	Mode           string                 `json:"mode"`
	BankIdx        int                    `json:"bank_idx"`
	TimeLimit      int                    `json:"time_limit"`    // seconds
	Scoring        bool                   `json:"scoring"`       // 是否启用计分
	ScorePerMode   map[string]float64     `json:"score_per_mode"` // 各题型每小题分值
	MultiScoreMode string                 `json:"multi_score_mode"` // strict|loose
	Ts             int64                  `json:"ts"`
	ExpiresAt      int64                  `json:"expires_at"`
}

const (
	rateLimit  = 120
	rateWindow = 60 * time.Second
)

func New(cfg Config) *Server {
	// Normalise: if legacy single-bank fields are used, wrap into Banks slice.
	if len(cfg.Banks) == 0 && len(cfg.Questions) > 0 {
		cfg.Banks = []BankEntry{{
			Path:          cfg.BankPath,
			Password:      cfg.Password,
			Questions:     cfg.Questions,
			DB:            cfg.DB,
			RecordEnabled: cfg.RecordEnabled,
		}}
	}
	tok := make([]byte, 16)
	rand.Read(tok)
	ver := make([]byte, 4)
	rand.Read(ver)
	s := &Server{
		cfg:          cfg,
		sessionToken: hex.EncodeToString(tok),
		assetVer:     hex.EncodeToString(ver),
		mux:          http.NewServeMux(),
		rateBuckets:  map[string][]time.Time{},
	}
	// 初始化 Web Push
	pushStores     := map[string]*pushStore{}
	pushTestBuckets := map[string][]time.Time{}
	for _, b := range cfg.Banks {
		pushStores[b.Path] = newPushStore()
	}
	s.pushStores     = pushStores
	s.pushTestBuckets = pushTestBuckets
	s.examSessions    = map[string]*examSession{}
	s.shareTokens     = map[string]*shareConfig{}
	if keys, err := generateVAPIDKeys(); err == nil {
		s.vapidKeys = keys
	} else {
		log.Printf("[push] VAPID 密钥生成失败: %v", err)
	}

	s.registerRoutes()
	s.startDailyPushScheduler()
	return s
}

func (s *Server) ListenAndServe() error {
	addr := fmt.Sprintf("%s:%d", s.cfg.Host, s.cfg.Port)
	s.httpServer = &http.Server{
		Addr:         addr,
		Handler:      s,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// 优雅关闭：监听信号
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	errCh := make(chan error, 1)
	go func() {
		log.Printf("Server listening on %s", addr)
		if err := s.httpServer.ListenAndServe(); err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case sig := <-stop:
		log.Printf("Received signal %v, shutting down...", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.httpServer.Shutdown(ctx); err != nil {
			log.Printf("Shutdown error: %v", err)
		}
		s.Close()
		log.Println("Server stopped")
	}
	return nil
}

// Close closes all database connections.
func (s *Server) Close() {
	for _, b := range s.cfg.Banks {
		if b.DB != nil {
			b.DB.Close()
		}
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	wrapped := &responseWriterWrapper{ResponseWriter: w, statusCode: 200}
	defer func() {
		logRequest(r, wrapped.statusCode, time.Since(start))
	}()

	auth.ApplySecurityHeaders(wrapped)

	// 健康检查端点不需要认证
	if r.URL.Path == "/api/health" {
		s.mux.ServeHTTP(wrapped, r)
		return
	}

	if r.URL.Path == "/auth" && r.Method == http.MethodPost {
		if s.cfg.AccessCode != "" {
			ip := remoteIP(r)
			if ok, retry := auth.CheckBruteForce(ip); !ok {
				minutes := (retry + 59) / 60
				msg := fmt.Sprintf("尝试次数过多，请 %d 分钟后重试", minutes)
				if retry < 60 {
					msg = fmt.Sprintf("尝试次数过多，请 %d 秒后重试", retry)
				}
				w := wrapped
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusTooManyRequests)
				io.WriteString(w, auth.RenderPINPage("医考练习", msg, s.cfg.PinLen, "", ""))
				return
			}
		}
		s.mux.ServeHTTP(wrapped, r)
		return
	}
	if !s.validHost(r) {
		jsonError(wrapped, "Forbidden", http.StatusForbidden)
		return
	}
	// PWA 必需资源不要求认证：SW、manifest、图标若被拦截会导致 PWA 完全失效
	pwaPublic := r.URL.Path == "/sw.js" ||
		r.URL.Path == "/manifest.json" ||
		r.URL.Path == "/static/icon.svg" ||
		r.URL.Path == "/static/icon-192.png" ||
		r.URL.Path == "/static/icon-512.png" ||
		r.URL.Path == "/api/push/vapid-key"
	if s.cfg.AccessCode != "" && !pwaPublic && !auth.IsAuthenticated(r, s.cfg.CookieSecret, s.cfg.AccessCode) {
		if (r.URL.Path == "/" || r.URL.Path == "") && r.Method == http.MethodGet {
			wrapped.Header().Set("Content-Type", "text/html; charset=utf-8")
			var tok, svg string
			if auth.NeedsCaptcha(remoteIP(r)) {
				tok, svg = auth.NewCaptcha()
			}
			io.WriteString(wrapped, auth.RenderPINPage("医考练习", "", s.cfg.PinLen, tok, svg))
			return
		}
		jsonError(wrapped, "Unauthorized", http.StatusUnauthorized)
		return
	}
	// /api/debug 只需访问码，无需 Session Token（调试用，不含敏感操作）
	if strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Path != "/api/debug" {
		tok := r.Header.Get("X-Session-Token")
		if subtle.ConstantTimeCompare([]byte(tok), []byte(s.sessionToken)) != 1 {
			jsonError(wrapped, "Unauthorized", http.StatusUnauthorized)
			return
		}
		if !s.checkRate(remoteIP(r)) {
			jsonError(wrapped, "Too Many Requests", http.StatusTooManyRequests)
			return
		}
	}
	s.mux.ServeHTTP(wrapped, r)
}

func (s *Server) registerRoutes() {
	m := s.mux
	m.HandleFunc("GET /{$}", s.handleIndex)
	m.HandleFunc("GET /editor", s.handleEditor)
	m.HandleFunc("POST /auth", s.handleAuth)
	m.HandleFunc("GET /api/health", s.handleHealth)
	if s.cfg.Assets != nil {
		sub, err := fs.Sub(s.cfg.Assets, "assets/static")
		if err == nil {
			m.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(sub))))
		}
		m.HandleFunc("GET /sw.js", func(w http.ResponseWriter, r *http.Request) {
			data, err := fs.ReadFile(s.cfg.Assets, "assets/static/sw.js")
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/javascript")
			w.Header().Set("Service-Worker-Allowed", "/")
			w.Header().Set("Cache-Control", "no-store")
			w.Write(data)
		})
		m.HandleFunc("GET /manifest.json", func(w http.ResponseWriter, r *http.Request) {
			data, err := fs.ReadFile(s.cfg.Assets, "assets/static/manifest.json")
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/manifest+json")
			w.Write(data)
		})
		// 图标：由路由动态生成，无需预置静态文件。
		// PWA 安装提示要求 PNG 格式（Chrome 对 SVG only 不触发 beforeinstallprompt）。
		m.HandleFunc("GET /static/icon.svg", s.handleIconSVG)
		m.HandleFunc("GET /static/icon-192.png", s.handleIcon192PNG)
		m.HandleFunc("GET /static/icon-512.png", s.handleIcon512PNG)
	}
	// Multi-bank listing endpoint
	m.HandleFunc("GET /api/banks", s.handleBanks)
	// Per-bank endpoints – all accept ?bank=N (default 0)
	m.HandleFunc("GET /api/info", s.handleInfo)
	m.HandleFunc("GET /api/questions", s.handleQuestions)
	m.HandleFunc("GET /api/question/", s.handleQuestion)
	m.HandleFunc("POST /api/question", s.handleCreateQuestion)
	m.HandleFunc("DELETE /api/question/", s.handleDeleteQuestion)
	m.HandleFunc("POST /api/question/", s.handleQuestionAction)
	m.HandleFunc("PUT /api/subquestion/", s.handleUpdateSubQuestion)
	m.HandleFunc("DELETE /api/subquestion/", s.handleDeleteSubQuestion)
	m.HandleFunc("POST /api/replace/preview", s.handleReplacePreview)
	m.HandleFunc("POST /api/replace", s.handleReplace)
	m.HandleFunc("POST /api/save", s.handleSave)
	m.HandleFunc("POST /api/record", s.handleRecord)
	m.HandleFunc("GET /api/record/status", s.handleRecordStatus)
	m.HandleFunc("POST /api/record/clear", s.handleRecordClear)
	m.HandleFunc("POST /api/record/migrate", s.handleRecordMigrate)
	m.HandleFunc("GET /api/history", s.handleHistory)
	m.HandleFunc("DELETE /api/session/", s.handleDeleteSession)
	m.HandleFunc("POST /api/calculate", s.handleCalculate)
		// Web Push
		m.HandleFunc("GET /api/push/vapid-key", s.handleVapidKey)
		m.HandleFunc("POST /api/push/subscribe", s.handlePushSubscribe)
		m.HandleFunc("DELETE /api/push/subscribe", s.handlePushUnsubscribe)
		m.HandleFunc("POST /api/push/test", s.handlePushTest)
	m.HandleFunc("GET /api/stats", s.handleStats)
	m.HandleFunc("GET /api/review/due", s.handleReviewDue)
	m.HandleFunc("GET /api/wrongbook", s.handleWrongbook)
	m.HandleFunc("POST /api/sync", s.handleSync)
	m.HandleFunc("GET /api/debug", s.handleDebug)
	m.HandleFunc("GET /api/sync/status", s.handleSyncStatus)
	m.HandleFunc("GET /api/exam/reveal", s.handleExamReveal)
	m.HandleFunc("POST /api/exam/share", s.handleExamShare)
	m.HandleFunc("GET /api/exam/join", s.handleExamJoin)
}

// ── Bank selection ────────────────────────────────────────────────────

// bankForReq returns the BankEntry for the request's ?bank=N param.
func (s *Server) bankForReq(r *http.Request) (*BankEntry, int, bool) {
	idxStr := r.URL.Query().Get("bank")
	idx := 0
	if idxStr != "" {
		var err error
		idx, err = strconv.Atoi(idxStr)
		if err != nil || idx < 0 || idx >= len(s.cfg.Banks) {
			return nil, 0, false
		}
	}
	if idx >= len(s.cfg.Banks) {
		return nil, 0, false
	}
	return &s.cfg.Banks[idx], idx, true
}

// ── Page handlers ─────────────────────────────────────────────────────

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
	ip   := remoteIP(r)

	// 如果需要验证码，先校验
	if auth.NeedsCaptcha(ip) {
		token  := r.FormValue("captcha_token")
		answer := r.FormValue("captcha_answer")
		if !auth.VerifyCaptcha(token, answer, ip) {
			// 验证码错误：重新生成并展示，不计入访问码失败次数
			tok, svg := auth.NewCaptcha()
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			io.WriteString(w, auth.RenderPINPage("医考练习", "验证码错误，请重新计算", s.cfg.PinLen, tok, svg))
			return
		}
	}

	if s.cfg.AccessCode == "" || code == s.cfg.AccessCode {
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
	})
}

func (s *Server) handleQuestions(w http.ResponseWriter, r *http.Request) {
	b, _, ok := s.bankForReq(r)
	if !ok {
		jsonError(w, "bank not found", http.StatusNotFound)
		return
	}
	q := r.URL.Query()

	if page := q.Get("page"); page != "" {
		s.handleEditorQuestions(w, q, b)
		return
	}

	limit, _ := strconv.Atoi(q.Get("limit"))
	var perMode map[string]int
	var perUnit map[string]int
	var difficulty map[string]float64
	var fpSet map[string]struct{}

	if v := q.Get("per_mode"); v != "" {
		json.Unmarshal([]byte(v), &perMode)
	}
	if v := q.Get("per_unit"); v != "" {
		json.Unmarshal([]byte(v), &perUnit)
	}
	if v := q.Get("difficulty"); v != "" {
		json.Unmarshal([]byte(v), &difficulty)
	}
	if v := q.Get("fingerprints"); v != "" {
		fpSet = map[string]struct{}{}
		for _, fp := range strings.Split(v, ",") {
			fpSet[fp] = struct{}{}
		}
	}

	rows, _ := selectQuestions(b.Questions, selectOpts{
		modes:      q["mode"],
		units:      q["unit"],
		limit:      limit,
		shuffle:    q.Get("shuffle") == "1",
		perMode:    perMode,
		perUnit:    perUnit,
		difficulty: difficulty,
		fpSet:      fpSet,
		rng:        newRNG(q.Get("seed")),
	})
	if rows == nil {
		rows = []sqFlat{}
	}

	// sealed=1: 考试防作弊模式，剥离答案和解析，服务端暂存
	if q.Get("sealed") == "1" && len(rows) > 0 {
		eid := hex.EncodeToString(func() []byte { b := make([]byte, 16); rand.Read(b); return b }())
		answers := make(map[string]examAnswer, len(rows))
		for i := range rows {
			answers[rows[i].Fingerprint] = examAnswer{Answer: rows[i].Answer, Discuss: rows[i].Discuss}
			rows[i].Answer = ""
			rows[i].Discuss = ""
			rows[i].Unit = "" // 考试模式隐藏章节信息，防止泄露提示
		}
		s.examMu.Lock()
		// 清理超过 24h 的旧 session（简单 LRU）
		now := time.Now().Unix()
		for k, v := range s.examSessions {
			if now-v.ts > 86400 { delete(s.examSessions, k) }
		}
		s.examSessions[eid] = &examSession{answers: answers, ts: now}
		s.examMu.Unlock()
		jsonOK(w, map[string]any{"total": len(rows), "items": rows, "exam_id": eid})
		return
	}

	jsonOK(w, map[string]any{"total": len(rows), "items": rows})
}

func (s *Server) handleEditorQuestions(w http.ResponseWriter, q url.Values, b *BankEntry) {
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	perPage, _ := strconv.Atoi(q.Get("per_page"))
	if perPage < 1 || perPage > 100 {
		perPage = 50
	}
	searchQ := q.Get("q")
	fpFilter := q.Get("fp")
	modeFilter := q.Get("mode")
	unitFilter := q.Get("unit")
	hasAI := q.Get("has_ai") == "1"
	missing := q.Get("missing") == "1"

	var allRows []sqFlat
	for qi, question := range b.Questions {
		for si, sq := range question.SubQuestions {
			row := sqFlat{
				QI: qi, SI: si,
				ID:   fmt.Sprintf("%d-%d", qi, si),
				Mode: question.Mode, Unit: question.Unit, Cls: question.Cls,
				Stem: question.Stem, SharedOptions: question.SharedOptions,
				Text: sq.Text, Options: sq.Options,
				Answer: sq.EffAnswer(), Discuss: sq.EffDiscuss(),
				Point: sq.Point, Rate: sq.Rate,
				HasAI:       sq.AIAnswer != "" || sq.AIDiscuss != "",
				Fingerprint: question.Fingerprint,
			}
			if modeFilter != "" && question.Mode != modeFilter {
				continue
			}
			if unitFilter != "" && !strings.Contains(question.Unit, unitFilter) {
				continue
			}
			if fpFilter != "" && question.Fingerprint != fpFilter {
				continue
			}
			if hasAI && sq.AIAnswer == "" && sq.AIDiscuss == "" {
				continue
			}
			if missing && (sq.Answer != "" || sq.Discuss != "") {
				continue
			}
			if searchQ != "" {
				text := strings.ToLower(question.Stem + sq.Text + sq.Discuss)
				if !strings.Contains(text, strings.ToLower(searchQ)) {
					continue
				}
			}
			allRows = append(allRows, row)
		}
	}

	total := len(allRows)
	pages := (total + perPage - 1) / perPage
	if pages < 1 {
		pages = 1
	}
	start := (page - 1) * perPage
	end := start + perPage
	if start > total {
		start = total
	}
	if end > total {
		end = total
	}
	items := []sqFlat{}
	if start < end {
		items = allRows[start:end]
	}
	jsonOK(w, map[string]any{
		"page":  page,
		"pages": pages,
		"total": total,
		"items": items,
	})
}

func (s *Server) handleQuestion(w http.ResponseWriter, r *http.Request) {
	b, _, ok := s.bankForReq(r)
	if !ok {
		jsonError(w, "bank not found", http.StatusNotFound)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/question/")
	qi, err := strconv.Atoi(path)
	if err != nil || qi < 0 || qi >= len(b.Questions) {
		jsonError(w, "question not found", http.StatusNotFound)
		return
	}
	q := b.Questions[qi]
	subQuestions := make([]map[string]any, len(q.SubQuestions))
	for i, sq := range q.SubQuestions {
		subQuestions[i] = map[string]any{
			"text":           sq.Text,
			"options":        sq.Options,
			"answer":         sq.Answer,
			"discuss":        sq.Discuss,
			"point":          sq.Point,
			"rate":           sq.Rate,
			"error_prone":    sq.ErrorProne,
			"fingerprint":    q.Fingerprint,
			"ai_answer":      sq.AIAnswer,
			"ai_discuss":     sq.AIDiscuss,
			"ai_confidence":  sq.AIConfidence,
			"ai_model":       sq.AIModel,
			"ai_status":      sq.AIStatus,
			"answer_source":  sq.AnswerSource(),
			"discuss_source": sq.DiscussSource(),
		}
	}
	jsonOK(w, map[string]any{
		"qi":             qi,
		"mode":           q.Mode,
		"unit":           q.Unit,
		"cls":            q.Cls,
		"stem":           q.Stem,
		"shared_options": q.SharedOptions,
		"sub_questions":  subQuestions,
	})
}

func (s *Server) handleCreateQuestion(w http.ResponseWriter, r *http.Request) {
	b, _, ok := s.bankForReq(r)
	if !ok {
		jsonError(w, "bank not found", http.StatusNotFound)
		return
	}
	var data map[string]any
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	mode, _ := data["mode"].(string)
	unit, _ := data["unit"].(string)
	if mode == "" {
		mode = "A1型题"
	}
	newQ := &models.Question{
		Mode: mode,
		Unit: unit,
		SubQuestions: []models.SubQuestion{{
			Text:    "",
			Options: []string{"A. ", "B. ", "C. ", "D. "},
			Answer:  "",
		}},
	}
	qi := len(b.Questions)
	b.Questions = append(b.Questions, newQ)
	jsonOK(w, map[string]any{"ok": true, "qi": qi})
}

func (s *Server) handleDeleteQuestion(w http.ResponseWriter, r *http.Request) {
	b, _, ok := s.bankForReq(r)
	if !ok {
		jsonError(w, "bank not found", http.StatusNotFound)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/question/")
	qi, err := strconv.Atoi(path)
	if err != nil || qi < 0 || qi >= len(b.Questions) {
		jsonError(w, "question not found", http.StatusNotFound)
		return
	}
	total := len(b.Questions) - 1
	b.Questions = append(b.Questions[:qi], b.Questions[qi+1:]...)
	jsonOK(w, map[string]any{"ok": true, "total": total})
}

func (s *Server) handleQuestionAction(w http.ResponseWriter, r *http.Request) {
	b, _, ok := s.bankForReq(r)
	if !ok {
		jsonError(w, "bank not found", http.StatusNotFound)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/question/")
	parts := strings.Split(path, "/")
	qi, err := strconv.Atoi(parts[0])
	if err != nil || qi < 0 || qi >= len(b.Questions) {
		jsonError(w, "question not found", http.StatusNotFound)
		return
	}
	if len(parts) != 2 || parts[1] != "subquestion" {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	si := len(b.Questions[qi].SubQuestions)
	b.Questions[qi].SubQuestions = append(b.Questions[qi].SubQuestions, models.SubQuestion{
		Text:    "",
		Options: []string{"A. ", "B. ", "C. ", "D. "},
		Answer:  "",
	})
	jsonOK(w, map[string]any{"ok": true, "si": si, "sub_total": len(b.Questions[qi].SubQuestions)})
}

func (s *Server) handleUpdateSubQuestion(w http.ResponseWriter, r *http.Request) {
	b, _, ok := s.bankForReq(r)
	if !ok {
		jsonError(w, "bank not found", http.StatusNotFound)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/subquestion/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 {
		jsonError(w, "invalid path", http.StatusBadRequest)
		return
	}
	qi, _ := strconv.Atoi(parts[0])
	si, _ := strconv.Atoi(parts[1])
	if qi < 0 || qi >= len(b.Questions) {
		jsonError(w, "question not found", http.StatusNotFound)
		return
	}
	q := b.Questions[qi]
	if si < 0 || si >= len(q.SubQuestions) {
		jsonError(w, "sub-question not found", http.StatusNotFound)
		return
	}
	var data map[string]any
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	sq := &q.SubQuestions[si]
	if v, ok := data["text"].(string); ok {
		sq.Text = v
	}
	if v, ok := data["answer"].(string); ok {
		sq.Answer = v
	}
	if v, ok := data["discuss"].(string); ok {
		sq.Discuss = v
	}
	if v, ok := data["point"].(string); ok {
		sq.Point = v
	}
	if v, ok := data["rate"].(string); ok {
		sq.Rate = v
	}
	if v, ok := data["options"].([]any); ok {
		opts := make([]string, len(v))
		for i, o := range v {
			if s, ok := o.(string); ok {
				opts[i] = s
			}
		}
		sq.Options = opts
	}
	if v, ok := data["mode"].(string); ok {
		q.Mode = v
	}
	if v, ok := data["unit"].(string); ok {
		q.Unit = v
	}
	if v, ok := data["cls"].(string); ok {
		q.Cls = v
	}
	if v, ok := data["stem"].(string); ok {
		q.Stem = v
	}
	if v, ok := data["shared_options"].([]any); ok {
		opts := make([]string, len(v))
		for i, o := range v {
			if s, ok := o.(string); ok {
				opts[i] = s
			}
		}
		q.SharedOptions = opts
	}
	jsonOK(w, map[string]any{"ok": true})
}

func (s *Server) handleDeleteSubQuestion(w http.ResponseWriter, r *http.Request) {
	b, _, ok := s.bankForReq(r)
	if !ok {
		jsonError(w, "bank not found", http.StatusNotFound)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/subquestion/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 {
		jsonError(w, "invalid path", http.StatusBadRequest)
		return
	}
	qi, _ := strconv.Atoi(parts[0])
	si, _ := strconv.Atoi(parts[1])
	if qi < 0 || qi >= len(b.Questions) {
		jsonError(w, "question not found", http.StatusNotFound)
		return
	}
	q := b.Questions[qi]
	if si < 0 || si >= len(q.SubQuestions) {
		jsonError(w, "sub-question not found", http.StatusNotFound)
		return
	}
	q.SubQuestions = append(q.SubQuestions[:si], q.SubQuestions[si+1:]...)
	jsonOK(w, map[string]any{"ok": true, "sub_total": len(q.SubQuestions)})
}

func (s *Server) handleReplacePreview(w http.ResponseWriter, r *http.Request) {
	b, _, ok := s.bankForReq(r)
	if !ok {
		jsonError(w, "bank not found", http.StatusNotFound)
		return
	}
	var data map[string]any
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	find, _ := data["find"].(string)
	fields, _ := data["fields"].([]any)
	mode, _ := data["mode"].(string)
	unit, _ := data["unit"].(string)
	limit, _ := data["limit"].(float64)
	if find == "" || len(fields) == 0 {
		jsonOK(w, map[string]any{"hits": []any{}})
		return
	}
	if limit == 0 {
		limit = 30
	}
	var hits []map[string]any
	for qi, q := range b.Questions {
		if mode != "" && q.Mode != mode {
			continue
		}
		if unit != "" && !strings.Contains(q.Unit, unit) {
			continue
		}
		for si, sq := range q.SubQuestions {
			hit := false
			preview := ""
			for _, f := range fields {
				field, _ := f.(string)
				var text string
				switch field {
				case "text":
					text = sq.Text
				case "discuss":
					text = sq.Discuss
				case "answer":
					text = sq.Answer
				case "stem":
					text = q.Stem
				}
				if strings.Contains(text, find) {
					hit = true
					preview = text
					break
				}
			}
			if hit {
				hits = append(hits, map[string]any{"qi": qi, "si": si, "preview": preview})
				if len(hits) >= int(limit) {
					break
				}
			}
		}
		if len(hits) >= int(limit) {
			break
		}
	}
	jsonOK(w, map[string]any{"hits": hits})
}

func (s *Server) handleReplace(w http.ResponseWriter, r *http.Request) {
	b, _, ok := s.bankForReq(r)
	if !ok {
		jsonError(w, "bank not found", http.StatusNotFound)
		return
	}
	var data map[string]any
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	find, _ := data["find"].(string)
	replace, _ := data["replace"].(string)
	fields, _ := data["fields"].([]any)
	mode, _ := data["mode"].(string)
	unit, _ := data["unit"].(string)
	if find == "" || len(fields) == 0 {
		jsonError(w, "find and fields required", http.StatusBadRequest)
		return
	}
	replaced := 0
	for _, q := range b.Questions {
		if mode != "" && q.Mode != mode {
			continue
		}
		if unit != "" && !strings.Contains(q.Unit, unit) {
			continue
		}
		for i := range q.SubQuestions {
			sq := &q.SubQuestions[i]
			for _, f := range fields {
				field, _ := f.(string)
				switch field {
				case "text":
					sq.Text = strings.ReplaceAll(sq.Text, find, replace)
				case "discuss":
					sq.Discuss = strings.ReplaceAll(sq.Discuss, find, replace)
				case "answer":
					sq.Answer = strings.ReplaceAll(sq.Answer, find, replace)
				case "stem":
					q.Stem = strings.ReplaceAll(q.Stem, find, replace)
				}
			}
			replaced++
		}
	}
	jsonOK(w, map[string]any{"ok": true, "replaced": replaced})
}

func (s *Server) handleSave(w http.ResponseWriter, r *http.Request) {
	b, _, ok := s.bankForReq(r)
	if !ok {
		jsonError(w, "bank not found", http.StatusNotFound)
		return
	}
	// PG 模式：写回数据库
	if b.PgStore != nil {
		_, err := b.PgStore.ImportBank(r.Context(), b.bankName(), "editor", b.Questions)
		if err != nil {
			jsonError(w, fmt.Sprintf("保存到数据库失败: %v", err), http.StatusInternalServerError)
			return
		}
		jsonOK(w, map[string]any{"ok": true, "path": "PostgreSQL", "count": len(b.Questions)})
		return
	}
	// .mqb 文件模式
	if b.Path == "" {
		jsonError(w, "未配置题库路径，无法保存", http.StatusBadRequest)
		return
	}
	outPath, err := bank.SaveBank(b.Questions, b.Path, b.Password, true, 6)
	if err != nil {
		jsonError(w, fmt.Sprintf("保存失败: %v", err), http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]any{"ok": true, "path": outPath, "count": len(b.Questions)})
}

func (s *Server) handleRecord(w http.ResponseWriter, r *http.Request) {
	b, _, ok := s.bankForReq(r)
	if !ok {
		jsonError(w, "bank not found", http.StatusNotFound)
		return
	}
	if !b.RecordEnabled {
		jsonOK(w, map[string]any{"ok": true, "skipped": true})
		return
	}
	var data map[string]any
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil || data == nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	uid := getUserID(r)
	// PostgreSQL 模式
	if b.PgStore != nil {
		data["bank_id"] = b.BankID // inject real PG bank_id (client sends array index)
		sessions := []map[string]any{data}
		done, _ := b.PgStore.RecordSessionsBatch(r.Context(), sessions, uid)
		if len(done) == 0 {
			jsonError(w, "记录写入失败", http.StatusInternalServerError)
			return
		}
		jsonOK(w, map[string]any{"ok": true})
		return
	}
	// SQLite 模式
	if b.DB == nil {
		jsonError(w, "进度数据库未初始化", http.StatusServiceUnavailable)
		return
	}
	if err := progress.RecordSession(b.DB, data, uid); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]any{"ok": true})
}

func (s *Server) handleRecordStatus(w http.ResponseWriter, r *http.Request) {
	b, _, ok := s.bankForReq(r)
	if !ok {
		jsonError(w, "bank not found", http.StatusNotFound)
		return
	}
	uid := getUserID(r)
	// Extra fields for client-side diagnostics

	jsonOK(w, map[string]any{
		"enabled":   b.RecordEnabled,
		"db_ready":  b.DB != nil || b.PgStore != nil,
		"user_id":   uid,
		"is_legacy": uid == progress.LegacyUser,
	})
}

func (s *Server) handleRecordClear(w http.ResponseWriter, r *http.Request) {
	b, _, ok := s.bankForReq(r)
	if !ok {
		jsonError(w, "bank not found", http.StatusNotFound)
		return
	}
	uid := getUserID(r)
	if b.PgStore != nil {
		deleted := b.PgStore.ClearUserData(r.Context(), uid, b.BankID)
		jsonOK(w, map[string]any{"ok": true, "deleted": deleted})
		return
	}
	if b.DB == nil {
		jsonOK(w, map[string]any{"ok": true, "deleted": map[string]int{"attempts":0,"sessions":0,"sm2_cards":0}})
		return
	}
	jsonOK(w, map[string]any{"ok": true, "deleted": progress.ClearUserData(b.DB, uid)})
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	b, _, ok := s.bankForReq(r)
	if !ok {
		jsonError(w, "bank not found", http.StatusNotFound)
		return
	}
	uid := getUserID(r)
	ctx := r.Context()
	if b.PgStore != nil {
		clientDate := r.URL.Query().Get("date")
		ov := b.PgStore.GetOverallStats(ctx, uid, b.BankID, clientDate)
		accuracy := 0
		if ov.Attempts > 0 { accuracy = int(math.Round(float64(ov.Correct)/float64(ov.Attempts)*100)) }
		wrongTopics := len(b.PgStore.GetWrongFingerprints(ctx, uid, b.BankID, 10000))
		jsonOK(w, map[string]any{
			"overall": map[string]any{
				"total_attempts": ov.Attempts, "correct": ov.Correct,
				"wrong_attempts": ov.Wrong, "accuracy": accuracy,
				"sessions": ov.Sessions, "due_today": ov.DueToday,
				"wrong_topics": wrongTopics,
			},
			"history": b.PgStore.GetHistory(ctx, uid, b.BankID, 30),
			"units":   b.PgStore.GetUnitStats(ctx, uid, b.BankID),
		})
		return
	}
	if b.DB == nil {
		jsonOK(w, map[string]any{"overall": map[string]any{}, "history": nil, "units": nil})
		return
	}
	bankFPs := make([]string, 0, len(b.Questions))
	for i := range b.Questions { bankFPs = append(bankFPs, b.Questions[i].Fingerprint) }
	clientDate := r.URL.Query().Get("date")
	jsonOK(w, map[string]any{
		"overall": progress.GetOverallStatsByFP(b.DB, uid, bankFPs, clientDate),
		"history": progress.GetHistoryByFP(b.DB, uid, bankFPs, 30),
		"units":   progress.GetUnitStatsByFP(b.DB, uid, bankFPs),
	})
}

func (s *Server) handleReviewDue(w http.ResponseWriter, r *http.Request) {
	b, _, ok := s.bankForReq(r)
	if !ok {
		jsonError(w, "bank not found", http.StatusNotFound)
		return
	}
	uid := getUserID(r)
	// Accept client's local date (YYYY-MM-DD) to avoid timezone mismatch.
	// e.g. users in UTC+8 would see no due items until 8AM if server uses UTC.
	clientDate := r.URL.Query().Get("date")
	if b.PgStore != nil {
		jsonOK(w, map[string]any{"fingerprints": b.PgStore.GetDueFingerprints(r.Context(), uid, b.BankID, clientDate)})
		return
	}
	if b.DB == nil {
		jsonOK(w, map[string]any{"fingerprints": nil})
		return
	}
	bankFPs2 := make([]string, 0, len(b.Questions))
	for i := range b.Questions { bankFPs2 = append(bankFPs2, b.Questions[i].Fingerprint) }
	jsonOK(w, map[string]any{"fingerprints": progress.GetDueFingerprintsByFP(b.DB, uid, bankFPs2, clientDate)})
}

func (s *Server) handleWrongbook(w http.ResponseWriter, r *http.Request) {
	b, _, ok := s.bankForReq(r)
	if !ok {
		jsonError(w, "bank not found", http.StatusNotFound)
		return
	}
	uid := getUserID(r)
	var entries []progress.WrongEntry
	if b.PgStore != nil {
		for _, e := range b.PgStore.GetWrongFingerprints(r.Context(), uid, b.BankID, 300) {
			entries = append(entries, progress.WrongEntry{
				Fingerprint: e.Fingerprint, Total: e.Total,
				Correct: e.Correct, Wrong: e.Wrong, Accuracy: e.Accuracy,
			})
		}
	} else if b.DB == nil {
		jsonOK(w, map[string]any{"items": nil})
		return
	} else {
		wbFPs := make([]string, 0, len(b.Questions))
		for i := range b.Questions { wbFPs = append(wbFPs, b.Questions[i].Fingerprint) }
		entries = progress.GetWrongFingerprintsByFP(b.DB, uid, wbFPs, 300)
	}

	// 构建 fingerprint → question 索引，为每条错题附上题目文字
	type wbItem struct {
		progress.WrongEntry
		Text          string   `json:"text"`
		Stem          string   `json:"stem,omitempty"`
		Answer        string   `json:"answer,omitempty"`
		Discuss       string   `json:"discuss,omitempty"`
		Unit          string   `json:"unit,omitempty"`
		Options       []string `json:"options,omitempty"`
		SharedOptions []string `json:"shared_options,omitempty"`
	}
	fpIdx := map[string]*models.Question{}
	for i := range b.Questions {
		q := b.Questions[i]
		fpIdx[q.Fingerprint] = q
	}
	// Only include entries whose fingerprint exists in the current bank's questions.
	// This is the definitive cross-bank filter: even if DB returns stale rows from
	// another bank (e.g. old data with bank_id=0), they are silently dropped here.
	items := make([]wbItem, 0, len(entries))
	for _, e := range entries {
		q, found := fpIdx[e.Fingerprint]
		if !found || len(q.SubQuestions) == 0 {
			// Fingerprint does not belong to current bank — skip entirely
			continue
		}
		sq := q.SubQuestions[0]
		items = append(items, wbItem{
			WrongEntry:    e,
			Text:          sq.Text,
			Stem:          q.Stem,
			Answer:        sq.EffAnswer(),
			Discuss:       sq.EffDiscuss(),
			Unit:          q.Unit,
			Options:       sq.Options,
			SharedOptions: q.SharedOptions,
		})
	}
	jsonOK(w, map[string]any{"items": items})
}

func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	b, _, ok := s.bankForReq(r)
	if !ok {
		jsonError(w, "bank not found", http.StatusNotFound)
		return
	}
	var payload struct {
		Sessions []map[string]any `json:"sessions"`
	}
	json.NewDecoder(r.Body).Decode(&payload)
	uid := getUserID(r)

	// sessions 中既不在 processed 也不在 skipped 的 → 写入失败，让客户端重试
	computeFailed := func(sessions []map[string]any, done, skipped []string) []map[string]any {
		okSet := make(map[string]bool, len(done)+len(skipped))
		for _, id := range done    { okSet[id] = true }
		for _, id := range skipped { okSet[id] = true }
		var failed []map[string]any
		for _, sess := range sessions {
			sid := fmt.Sprint(sess["id"])
			if !okSet[sid] {
				failed = append(failed, map[string]any{"session_id": sid})
			}
		}
		return failed
	}

	// PostgreSQL 模式
	if b.PgStore != nil {
		for i := range payload.Sessions {
			if payload.Sessions[i] == nil { payload.Sessions[i] = map[string]any{} }
			payload.Sessions[i]["bank_id"] = b.BankID
		}
		done, skipped := b.PgStore.RecordSessionsBatch(r.Context(), payload.Sessions, uid)
		failed := computeFailed(payload.Sessions, done, skipped)
		if len(failed) > 0 {
			log.Printf("[sync] PG uid=%s bank=%d processed=%d skipped=%d failed=%d",
				uid, b.BankID, len(done), len(skipped), len(failed))
		}
		jsonOK(w, map[string]any{"ok": true, "processed": done, "skipped": skipped, "failed": failed})
		return
	}
	// SQLite 模式
	if b.DB == nil {
		log.Printf("[sync] WARN: b.DB is nil, RecordEnabled=%v", b.RecordEnabled)
		jsonOK(w, map[string]any{"ok": false, "error": "DB not initialised"})
		return
	}
	for i := range payload.Sessions {
		if payload.Sessions[i] == nil { payload.Sessions[i] = map[string]any{} }
		payload.Sessions[i]["bank_id"] = b.BankID
	}
	log.Printf("[sync] uid=%s bank=%d sessions=%d", uid, b.BankID, len(payload.Sessions))
	done, skipped := progress.RecordSessionsBatch(b.DB, payload.Sessions, uid)
	failed := computeFailed(payload.Sessions, done, skipped)
	log.Printf("[sync] done=%v skipped=%v failed=%d", done, skipped, len(failed))
	jsonOK(w, map[string]any{"ok": true, "processed": done, "skipped": skipped, "failed": failed})
}

func (s *Server) handleSyncStatus(w http.ResponseWriter, r *http.Request) {
	b, _, ok := s.bankForReq(r)
	if !ok {
		jsonError(w, "bank not found", http.StatusNotFound)
		return
	}
	uid := getUserID(r)
	if b.PgStore != nil {
		jsonOK(w, b.PgStore.GetSyncStatus(r.Context(), uid, b.BankID))
		return
	}
	if b.DB == nil {
		jsonOK(w, map[string]any{"session_count": 0, "last_ts": nil})
		return
	}
	jsonOK(w, progress.GetSyncStatus(b.DB, uid))
}

// handleExamReveal 考试防作弊：提交后下发答案
func (s *Server) handleExamReveal(w http.ResponseWriter, r *http.Request) {
	eid := r.URL.Query().Get("id")
	if eid == "" {
		jsonError(w, "缺少 exam id", http.StatusBadRequest)
		return
	}
	s.examMu.Lock()
	sess, ok := s.examSessions[eid]
	if ok {
		delete(s.examSessions, eid) // 一次性使用
	}
	s.examMu.Unlock()
	if !ok {
		jsonError(w, "考试会话已过期或不存在", http.StatusNotFound)
		return
	}
	jsonOK(w, map[string]any{"answers": sess.answers})
}

// handleExamShare 生成试卷分享令牌
func (s *Server) handleExamShare(w http.ResponseWriter, r *http.Request) {
	_, bankIdx, ok := s.bankForReq(r)
	if !ok {
		jsonError(w, "bank not found", http.StatusNotFound)
		return
	}
	var body struct {
		Fingerprints   []string               `json:"fingerprints"`
		Mode           string                 `json:"mode"`
		TimeLimit      int                    `json:"time_limit"`
		Scoring        bool                   `json:"scoring"`
		ScorePerMode   map[string]float64     `json:"score_per_mode"`
		MultiScoreMode string                 `json:"multi_score_mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Fingerprints) == 0 {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	timeLimit := body.TimeLimit
	if timeLimit <= 0 {
		timeLimit = 90 * 60
	}
	if body.MultiScoreMode == "" {
		body.MultiScoreMode = "strict"
	}

	tok := make([]byte, 8)
	rand.Read(tok)
	token := hex.EncodeToString(tok)

	now := time.Now().Unix()
	expiresAt := now + int64(7*24*3600)

	cfg := &shareConfig{
		Fingerprints:   body.Fingerprints,
		Mode:           body.Mode,
		BankIdx:        bankIdx,
		TimeLimit:      timeLimit,
		Scoring:        body.Scoring,
		ScorePerMode:   body.ScorePerMode,
		MultiScoreMode: body.MultiScoreMode,
		Ts:             now,
		ExpiresAt:      expiresAt,
	}

	// Try to persist to PostgreSQL if available (golang-version + PG mode).
	// Falls back to in-memory storage otherwise.
	pgPersisted := false
	if bankIdx < len(s.cfg.Banks) {
		if b := &s.cfg.Banks[bankIdx]; b.PgStore != nil {
			if ps, ok2 := b.PgStore.(shareStorer); ok2 {
				if data, err := json.Marshal(cfg); err == nil {
					if err2 := ps.SaveShareTokenJSON(r.Context(), token, data); err2 == nil {
						pgPersisted = true
					}
				}
			}
		}
	}
	_ = pgPersisted

	s.shareMu.Lock()
	// Clean up expired in-memory tokens (7-day window)
	for k, v := range s.shareTokens {
		exp := v.ExpiresAt
		if exp == 0 {
			exp = v.Ts + int64(7*24*3600)
		}
		if now > exp {
			delete(s.shareTokens, k)
		}
	}
	if !pgPersisted {
		s.shareTokens[token] = cfg
	} else {
		// Keep a lightweight in-memory copy so same-process lookups are fast.
		s.shareTokens[token] = cfg
	}
	s.shareMu.Unlock()

	jsonOK(w, map[string]any{"ok": true, "token": token})
}

// handleExamJoin 加入分享试卷
func (s *Server) handleExamJoin(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		jsonError(w, "缺少 token", http.StatusBadRequest)
		return
	}

	s.shareMu.Lock()
	cfg, ok := s.shareTokens[token]
	s.shareMu.Unlock()

	// 内存中未命中 → 尝试从 PG 加载（服务重启后恢复）
	if !ok {
		cfg = s.loadShareFromPG(r.Context(), token)
		if cfg != nil {
			s.shareMu.Lock()
			s.shareTokens[token] = cfg
			s.shareMu.Unlock()
		}
	}

	if cfg == nil {
		jsonError(w, "分享链接已过期或无效", http.StatusNotFound)
		return
	}

	// 校验 7 天有效期
	now := time.Now().Unix()
	expiresAt := cfg.ExpiresAt
	if expiresAt == 0 {
		expiresAt = cfg.Ts + int64(7*24*3600)
	}
	if now > expiresAt {
		s.shareMu.Lock()
		delete(s.shareTokens, token)
		s.shareMu.Unlock()
		s.deleteShareFromPG(r.Context(), token)
		jsonError(w, "分享链接已过期（7天有效期）", http.StatusNotFound)
		return
	}

	if cfg.BankIdx < 0 || cfg.BankIdx >= len(s.cfg.Banks) {
		jsonError(w, "题库不存在", http.StatusNotFound)
		return
	}
	b := &s.cfg.Banks[cfg.BankIdx]
	fpSet := map[string]struct{}{}
	for _, fp := range cfg.Fingerprints { fpSet[fp] = struct{}{} }
	rows, _ := selectQuestions(b.Questions, selectOpts{fpSet: fpSet})
	if rows == nil { rows = []sqFlat{} }

	// sealed 模式：剥离答案，服务端暂存。
	// exam_done 是前端交卷后的内部状态，功能上等同于 exam，同样需要密封答案。
	// 返回给客户端时统一使用 "exam"，让接收方直接进入考试模式。
	var eid string
	isExamMode := cfg.Mode == "exam" || cfg.Mode == "exam_done"
	if isExamMode && len(rows) > 0 {
		eidBytes := make([]byte, 16); rand.Read(eidBytes)
		eid = hex.EncodeToString(eidBytes)
		answers := make(map[string]examAnswer, len(rows))
		for i := range rows {
			answers[rows[i].Fingerprint] = examAnswer{Answer: rows[i].Answer, Discuss: rows[i].Discuss}
			rows[i].Answer = ""
			rows[i].Discuss = ""
		}
		s.examMu.Lock()
		for k, v := range s.examSessions {
			if now-v.ts > 86400 { delete(s.examSessions, k) }
		}
		s.examSessions[eid] = &examSession{answers: answers, ts: now}
		s.examMu.Unlock()
	}

	timeLimit := cfg.TimeLimit
	if timeLimit <= 0 {
		timeLimit = 90 * 60
	}

	// 返回给客户端的 mode 统一规范：exam/exam_done → "exam"，其他保持原值
	outMode := cfg.Mode
	if isExamMode {
		outMode = "exam"
	}

	jsonOK(w, map[string]any{
		"items": rows, "total": len(rows), "mode": outMode,
		"exam_id": eid, "time_limit": timeLimit,
		"bank_idx":         cfg.BankIdx,
		"scoring":          cfg.Scoring,
		"score_per_mode":   cfg.ScorePerMode,
		"multi_score_mode": cfg.MultiScoreMode,
	})
}

// loadShareFromPG 尝试从 PostgreSQL 读取 share token（服务重启后恢复）。
func (s *Server) loadShareFromPG(ctx context.Context, token string) *shareConfig {
	for i := range s.cfg.Banks {
		if b := &s.cfg.Banks[i]; b.PgStore != nil {
			if ps, ok := b.PgStore.(shareStorer); ok {
				data := ps.LoadShareTokenJSON(ctx, token)
				if data == nil {
					return nil
				}
				var cfg shareConfig
				if err := json.Unmarshal(data, &cfg); err != nil {
					return nil
				}
				return &cfg
			}
			break
		}
	}
	return nil
}

// deleteShareFromPG 从 PostgreSQL 删除已过期的 share token。
func (s *Server) deleteShareFromPG(ctx context.Context, token string) {
	for i := range s.cfg.Banks {
		if b := &s.cfg.Banks[i]; b.PgStore != nil {
			if ps, ok := b.PgStore.(shareStorer); ok {
				ps.DeleteShareToken(ctx, token)
			}
			break
		}
	}
}

// ── Icon handlers ─────────────────────────────────────────────────────

// handleIconSVG 返回 SVG 格式图标（用于非 PWA 场景展示）。
func (s *Server) handleIconSVG(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	data, err := fs.ReadFile(s.cfg.Assets, "assets/static/icon.svg")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Write(data) //nolint:errcheck
}

// generateIcons 从嵌入的静态资源中加载品牌图标 PNG，缓存到 Server。
func (s *Server) generateIcons() {
	s.iconOnce.Do(func() {
		if data, err := fs.ReadFile(s.cfg.Assets, "assets/static/icon-192.png"); err == nil {
			s.icon192 = data
		}
		if data, err := fs.ReadFile(s.cfg.Assets, "assets/static/icon-512.png"); err == nil {
			s.icon512 = data
		}
	})
}

func (s *Server) handleIcon192PNG(w http.ResponseWriter, r *http.Request) {
	s.generateIcons()
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(s.icon192) //nolint:errcheck
}

func (s *Server) handleIcon512PNG(w http.ResponseWriter, r *http.Request) {
	s.generateIcons()
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(s.icon512) //nolint:errcheck
}

// ── Data migration ─────────────────────────────────────────────────────

// handleRecordMigrate 将旧 UID 的学习记录合并到当前 UID。
// 入口刻意做得隐蔽（统计页用户 ID 旁的小链接），防止误操作。
func (s *Server) handleRecordMigrate(w http.ResponseWriter, r *http.Request) {
	b, _, ok := s.bankForReq(r)
	if !ok {
		jsonError(w, "bank not found", http.StatusNotFound)
		return
	}
	if !b.RecordEnabled || b.DB == nil {
		jsonError(w, "记录功能未启用", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		FromUID string `json:"from_uid"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.FromUID == "" {
		jsonError(w, "缺少 from_uid", http.StatusBadRequest)
		return
	}
	toUID := getUserID(r)
	if body.FromUID == toUID {
		jsonError(w, "来源 ID 与当前 ID 相同", http.StatusBadRequest)
		return
	}
	counts, err := progress.MigrateUserData(b.DB, body.FromUID, toUID)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]any{"ok": true, "migrated": counts})
}

// ── Question selection ─────────────────────────────────────────────────

type sqFlat struct {
	QI            int      `json:"qi"`
	SI            int      `json:"si"`
	ID            string   `json:"id"`
	Mode          string   `json:"mode"`
	Unit          string   `json:"unit"`
	Cls           string   `json:"cls"`
	Stem          string   `json:"stem"`
	SharedOptions []string `json:"shared_options"`
	Text          string   `json:"text"`
	Options       []string `json:"options"`
	Answer        string   `json:"answer"`
	Discuss       string   `json:"discuss"`
	Point         string   `json:"point"`
	Rate          string   `json:"rate"`
	HasAI         bool     `json:"has_ai"`
	Fingerprint   string   `json:"fingerprint"`
}

type selectOpts struct {
	modes      []string
	units      []string
	limit      int
	shuffle    bool
	perMode    map[string]int
	perUnit    map[string]int
	difficulty map[string]float64
	fpSet      map[string]struct{}
	rng        *lcgRNG
}

type group = []sqFlat

func selectQuestions(questions []*models.Question, opts selectOpts) ([]sqFlat, int) {
	var groups []group
	modeOrder := []string{}
	modeMap := map[string][]group{}

	for qi, q := range questions {
		if len(opts.modes) > 0 {
			found := false
			for _, m := range opts.modes {
				if q.Mode == m {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		if len(opts.units) > 0 {
			found := false
			for _, u := range opts.units {
				if u != "" && strings.Contains(q.Unit, u) {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		if opts.perUnit != nil {
			if _, ok := opts.perUnit[q.Unit]; !ok {
				continue
			}
		}
		if opts.fpSet != nil {
			if _, ok := opts.fpSet[q.Fingerprint]; !ok {
				continue
			}
		}
		var grp group
		shared := q.SharedOptions
		for si, sq := range q.SubQuestions {
			effOpts := sq.Options
			if len(effOpts) == 0 {
				effOpts = shared
			}
			grp = append(grp, sqFlat{
				QI: qi, SI: si, ID: fmt.Sprintf("%d-%d", qi, si),
				Mode: q.Mode, Unit: q.Unit, Cls: q.Cls,
				Stem: q.Stem, SharedOptions: shared,
				Text: sq.Text, Options: effOpts,
				Answer: sq.EffAnswer(), Discuss: sq.EffDiscuss(),
				Point: sq.Point, Rate: sq.Rate,
				HasAI:       sq.AIAnswer != "" || sq.AIDiscuss != "",
				Fingerprint: q.Fingerprint,
			})
		}
		if len(grp) == 0 {
			continue
		}
		groups = append(groups, grp)
		mk := q.Mode
		if _, ok := modeMap[mk]; !ok {
			modeOrder = append(modeOrder, mk)
		}
		modeMap[mk] = append(modeMap[mk], grp)
	}

	var resultGroups []group
	switch {
	case opts.perUnit != nil:
		unitOrder := []string{}
		unitMap := map[string][]group{}
		for _, grp := range groups {
			uk := grp[0].Unit
			if _, ok := unitMap[uk]; !ok {
				unitOrder = append(unitOrder, uk)
			}
			unitMap[uk] = append(unitMap[uk], grp)
		}
		reorder := map[string][]group{}
		for _, uk := range unitOrder {
			need, ok := opts.perUnit[uk]
			if !ok || need <= 0 {
				continue
			}
			pool := append([]group(nil), unitMap[uk]...)
			opts.rng.shuffle(pool)
			for _, grp := range greedyFill(pool, need) {
				mk := grp[0].Mode
				reorder[mk] = append(reorder[mk], grp)
			}
		}
		for _, mk := range modeOrder {
			resultGroups = append(resultGroups, reorder[mk]...)
		}

	case !opts.shuffle:
		for _, mk := range modeOrder {
			resultGroups = append(resultGroups, modeMap[mk]...)
		}
		if opts.limit > 0 {
			var cut []group
			n := 0
			for _, grp := range resultGroups {
				c := len(grp)
				if n+c > opts.limit {
					continue
				}
				cut = append(cut, grp)
				n += c
				if n >= opts.limit {
					break
				}
			}
			resultGroups = cut
		}

	case opts.perMode != nil:
		totalNeedPM := 0
		for _, mk := range modeOrder {
			need := opts.perMode[mk]
			if need <= 0 {
				continue
			}
			totalNeedPM += need
			pool := append([]group(nil), modeMap[mk]...)
			opts.rng.shuffle(pool)
			resultGroups = append(resultGroups, greedyFill(pool, need)...)
		}
		actualPM := 0
		for _, grp := range resultGroups {
			actualPM += len(grp)
		}
		if shortfallPM := totalNeedPM - actualPM; shortfallPM > 0 {
			pickedPM := map[string]bool{}
			for _, grp := range resultGroups {
				if len(grp) > 0 {
					pickedPM[grp[0].ID] = true
				}
			}
			for _, mk := range modeOrder {
				if shortfallPM <= 0 {
					break
				}
				for _, grp := range modeMap[mk] {
					if shortfallPM <= 0 {
						break
					}
					if !pickedPM[grp[0].ID] && len(grp) <= shortfallPM {
						resultGroups = append(resultGroups, grp)
						pickedPM[grp[0].ID] = true
						shortfallPM -= len(grp)
					}
				}
			}
		}

	default:
		modeSQTotal := map[string]int{}
		totalSQAll := 0
		for _, mk := range modeOrder {
			for _, grp := range modeMap[mk] {
				modeSQTotal[mk] += len(grp)
			}
			totalSQAll += modeSQTotal[mk]
		}
		totalNeed := opts.limit
		if totalNeed <= 0 {
			totalNeed = totalSQAll
		}
		quotas := distributeByRatio(totalNeed, modeSQTotal)
		for mk, total := range modeSQTotal {
			if total > 0 && quotas[mk] < 1 {
				quotas[mk] = 1
			}
		}
		overflow := -totalNeed
		for _, v := range quotas {
			overflow += v
		}
		if overflow > 0 {
			for _, mk := range modeOrder {
				if overflow <= 0 {
					break
				}
				red := quotas[mk] - 1
				if red <= 0 {
					continue
				}
				cut := min(red, overflow)
				quotas[mk] -= cut
				overflow -= cut
			}
		}
		for _, mk := range modeOrder {
			need := quotas[mk]
			if need <= 0 {
				continue
			}
			pool := append([]group(nil), modeMap[mk]...)
			opts.rng.shuffle(pool)
			resultGroups = append(resultGroups, greedyFill(pool, need)...)
		}
		actual := 0
		for _, grp := range resultGroups {
			actual += len(grp)
		}
		if shortfall := totalNeed - actual; shortfall > 0 {
			picked := map[string]bool{}
			for _, grp := range resultGroups {
				if len(grp) > 0 {
					picked[grp[0].ID] = true
				}
			}
			for _, mk := range modeOrder {
				if shortfall <= 0 {
					break
				}
				for _, grp := range modeMap[mk] {
					if shortfall <= 0 {
						break
					}
					if !picked[grp[0].ID] && len(grp) <= shortfall {
						resultGroups = append(resultGroups, grp)
						picked[grp[0].ID] = true
						shortfall -= len(grp)
					}
				}
			}
		}
	}

	var rows []sqFlat
	for _, grp := range resultGroups {
		rows = append(rows, grp...)
	}
	return rows, len(rows)
}

func greedyFill(pool []group, target int) []group {
	var picked []group
	n := 0
	var remaining []group
	for _, grp := range pool {
		c := len(grp)
		if n+c <= target {
			picked = append(picked, grp)
			n += c
			if n == target {
				return picked
			}
		} else {
			remaining = append(remaining, grp)
		}
	}
	for n < target && len(remaining) > 0 {
		gap := target - n
		bestIdx, bestC := -1, 0
		for i, grp := range remaining {
			c := len(grp)
			if c <= gap && c > bestC {
				bestC = c
				bestIdx = i
			}
		}
		if bestIdx < 0 {
			break
		}
		picked = append(picked, remaining[bestIdx])
		n += bestC
		remaining = append(remaining[:bestIdx], remaining[bestIdx+1:]...)
	}
	if n < target && len(remaining) > 0 {
		gap := target - n
		bestIdx := -1
		bestSize := 0
		for i, grp := range remaining {
			c := len(grp)
			if c > gap && (bestIdx == -1 || c < bestSize) {
				bestIdx = i
				bestSize = c
			}
		}
		if bestIdx >= 0 {
			truncated := make(group, gap)
			copy(truncated, remaining[bestIdx][:gap])
			picked = append(picked, truncated)
		}
	}
	return picked
}

func distributeByRatio(total int, weights map[string]int) map[string]int {
	wSum := 0
	for _, w := range weights {
		wSum += w
	}
	result := map[string]int{}
	if wSum == 0 {
		for k := range weights {
			result[k] = 0
		}
		return result
	}
	allocated := 0
	keys := make([]string, 0, len(weights))
	for k := range weights {
		keys = append(keys, k)
	}
	for i, k := range keys {
		if i == len(keys)-1 {
			v := total - allocated
			if v < 0 {
				v = 0
			}
			result[k] = v
		} else {
			n := (total*weights[k] + wSum/2) / wSum
			result[k] = n
			allocated += n
		}
	}
	return result
}


// ── Rate limiter ───────────────────────────────────────────────────────

func (s *Server) checkRate(ip string) bool {
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	now := time.Now()
	cutoff := now.Add(-rateWindow)
	bucket := s.rateBuckets[ip]
	fresh := bucket[:0]
	for _, t := range bucket {
		if t.After(cutoff) {
			fresh = append(fresh, t)
		}
	}
	if len(fresh) >= rateLimit {
		s.rateBuckets[ip] = fresh
		return false
	}
	s.rateBuckets[ip] = append(fresh, now)
	return true
}

func (s *Server) validHost(r *http.Request) bool {
	host := r.Host
	if s.cfg.Host == "127.0.0.1" || s.cfg.Host == "localhost" {
		allowed := map[string]bool{
			fmt.Sprintf("127.0.0.1:%d", s.cfg.Port): true,
			fmt.Sprintf("localhost:%d", s.cfg.Port): true,
			"127.0.0.1":                             true, "localhost": true,
		}
		return allowed[host]
	}
	if idx := strings.LastIndex(host, ":"); idx >= 0 {
		if p, err := strconv.Atoi(host[idx+1:]); err == nil && p != s.cfg.Port {
			return false
		}
	}
	return true
}

func getUserID(r *http.Request) string {
	// Prefer explicit header (sent by apiFetch, works through any proxy)
	if uid := r.Header.Get("X-User-ID"); uid != "" {
		return uid
	}
	// Fallback: cookie (same-origin direct access)
	c, err := r.Cookie("med_exam_uid")
	if err != nil {
		return progress.LegacyUser
	}
	return c.Value
}

func remoteIP(r *http.Request) string {
	// 优先读取反代传递的真实客户端 IP（nginx: proxy_set_header X-Real-IP $remote_addr）
	if ip := r.Header.Get("X-Real-IP"); ip != "" {
		return strings.TrimSpace(strings.SplitN(ip, ",", 2)[0])
	}
	// 兼容多级代理链（取最左侧，即原始客户端）
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.SplitN(xff, ",", 2)[0])
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}

func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

type lcgRNG struct{ state uint64 }

func newRNG(seed string) *lcgRNG {
	if seed == "" {
		n, _ := rand.Int(rand.Reader, new(big.Int).SetUint64(^uint64(0)))
		return &lcgRNG{state: n.Uint64()}
	}
	h := uint64(14695981039346656037)
	for _, b := range []byte(seed) {
		h ^= uint64(b)
		h *= 1099511628211
	}
	return &lcgRNG{state: h}
}

func (r *lcgRNG) next() uint64 {
	r.state = r.state*6364136223846793005 + 1442695040888963407
	return r.state
}

func (r *lcgRNG) shuffle(groups []group) {
	for i := len(groups) - 1; i > 0; i-- {
		j := int(r.next() % uint64(i+1))
		groups[i], groups[j] = groups[j], groups[i]
	}
}

// ── Logging middleware ─────────────────────────────────────────────────

type responseWriterWrapper struct {
	http.ResponseWriter
	statusCode int
	written    bool
}

func (w *responseWriterWrapper) WriteHeader(code int) {
	if !w.written {
		w.statusCode = code
		w.written = true
		w.ResponseWriter.WriteHeader(code)
	}
}

func (w *responseWriterWrapper) Write(b []byte) (int, error) {
	if !w.written {
		w.statusCode = 200
		w.written = true
	}
	return w.ResponseWriter.Write(b)
}

func logRequest(r *http.Request, status int, duration time.Duration) {
	ip := remoteIP(r)
	method := r.Method
	path := r.URL.Path
	if path == "" {
		path = "/"
	}
	// 静态资源简化显示
	if strings.HasPrefix(path, "/static/") {
		path = "/static/..."
	}
	log.Printf("%s %s %s %d %v", ip, method, path, status, duration.Round(time.Millisecond))
}

// ── Push API handlers ────────────────────────────────────────────────────

// GET /api/push/vapid-key  →  {"publicKey":"..."}
func (s *Server) handleVapidKey(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(map[string]string{"publicKey": s.vapidPublicKey()})
}

// POST /api/push/subscribe  body: PushSubscription JSON
func (s *Server) handlePushSubscribe(w http.ResponseWriter, r *http.Request) {
	b, _, ok := s.bankForReq(r)
	if !ok {
		jsonError(w, "bank not found", http.StatusNotFound)
		return
	}
	var body struct {
		PushSubscription
		UID string `json:"uid"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Endpoint == "" {
		jsonError(w, "invalid subscription", http.StatusBadRequest)
		return
	}
	store := s.pushStores[b.Path]
	if store == nil {
		jsonError(w, "push not available", http.StatusServiceUnavailable)
		return
	}
	store.add(&body.PushSubscription, body.UID)
	log.Printf("[push] 新订阅: uid=%s ep=%s…", body.UID, body.Endpoint[:min(40, len(body.Endpoint))])
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// DELETE /api/push/subscribe  body: {"endpoint":"..."}
func (s *Server) handlePushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	b, _, ok := s.bankForReq(r)
	if !ok {
		jsonError(w, "bank not found", http.StatusNotFound)
		return
	}
	var body struct{ Endpoint string `json:"endpoint"` }
	json.NewDecoder(r.Body).Decode(&body)
	if store := s.pushStores[b.Path]; store != nil && body.Endpoint != "" {
		store.remove(body.Endpoint)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}


// POST /api/push/test — 立即发送测试推送
// 支持两种模式：
//   ?uid=<用户ID>  → 只推给该用户
//   （无参数）     → 推给所有订阅者
// 每个 IP 限流：5次/小时，防止滥用。
func (s *Server) handlePushTest(w http.ResponseWriter, r *http.Request) {
	// 严格限流：每 IP 每小时最多 5 次
	ip := remoteIP(r)
	s.pushTestMu.Lock()
	now    := time.Now()
	cutoff := now.Add(-time.Hour)
	fresh  := s.pushTestBuckets[ip][:0]
	for _, t := range s.pushTestBuckets[ip] {
		if t.After(cutoff) {
			fresh = append(fresh, t)
		}
	}
	if len(fresh) >= 5 {
		s.pushTestBuckets[ip] = fresh
		s.pushTestMu.Unlock()
		jsonError(w, "请求过于频繁，每小时最多测试 5 次", http.StatusTooManyRequests)
		return
	}
	s.pushTestBuckets[ip] = append(fresh, now)
	s.pushTestMu.Unlock()

	if s.vapidKeys == nil {
		jsonError(w, "push not initialised", http.StatusServiceUnavailable)
		return
	}
	b, _, ok := s.bankForReq(r)
	if !ok {
		jsonError(w, "bank not found", http.StatusNotFound)
		return
	}
	store := s.pushStores[b.Path]
	if store == nil {
		jsonError(w, "push not available", http.StatusServiceUnavailable)
		return
	}

	payload, _ := json.Marshal(map[string]any{
		"title": "医考练习 · 测试通知",
		"body":  "推送功能正常 🎉 点击打开应用",
		"due":   0,
	})

	uid := r.URL.Query().Get("uid")
	var subs []*PushSubscription
	if uid != "" {
		// 指定用户
		sub := store.forUID(uid)
		if sub == nil {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			json.NewEncoder(w).Encode(map[string]any{"ok": false, "msg": "该用户暂无订阅"})
			return
		}
		subs = []*PushSubscription{sub}
	} else {
		// 所有订阅者
		subs = store.all()
	}

	if len(subs) == 0 {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "sent": 0, "msg": "暂无订阅者"})
		return
	}

	sent, failed, removed := 0, 0, 0
	for _, sub := range subs {
		err := sendPush(s.vapidKeys, sub, payload, "mailto:noreply@med-exam-kit")
		if err == nil {
			sent++
		} else if err == errSubscriptionGone {
			store.remove(sub.Endpoint)
			removed++
		} else {
			log.Printf("[push/test] 失败: %v", err)
			failed++
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(map[string]any{
		"ok": true, "sent": sent, "failed": failed, "removed": removed,
	})
}

// GET /api/history?bank=N&limit=50
// 返回服务端保存的完整历史记录（sessions 表），供主页展示
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	b, _, ok := s.bankForReq(r)
	if !ok {
		jsonError(w, "bank not found", http.StatusNotFound)
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	uid := getUserID(r)
	if b.PgStore != nil {
		jsonOK(w, map[string]any{"items": b.PgStore.GetHistory(r.Context(), uid, b.BankID, limit)})
		return
	}
	if b.DB == nil {
		jsonOK(w, map[string]any{"items": nil})
		return
	}
	jsonOK(w, map[string]any{
		// SQLite: filter history to sessions from this bank's fingerprints
		"items": progress.GetHistoryByFP(b.DB, uid, func() []string {
			fs := make([]string, 0, len(b.Questions))
			for i := range b.Questions { fs = append(fs, b.Questions[i].Fingerprint) }
			return fs
		}(), limit),
	})
}

// DELETE /api/session/{id}?bank=N
// 删除指定会话记录（只能删除自己的）
func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	b, _, ok := s.bankForReq(r)
	if !ok {
		jsonError(w, "bank not found", http.StatusNotFound)
		return
	}
	// 路径：/api/session/{id}
	sessionID := strings.TrimPrefix(r.URL.Path, "/api/session/")
	sessionID = strings.Trim(sessionID, "/")
	if sessionID == "" {
		jsonError(w, "session id required", http.StatusBadRequest)
		return
	}
	uid := getUserID(r)
	var ok2 bool
	if b.PgStore != nil {
		ok2 = b.PgStore.DeleteSession(r.Context(), sessionID, uid)
	} else if b.DB != nil {
		ok2 = progress.DeleteSession(b.DB, sessionID, uid)
	} else {
		// DB 不存在时视为「无需删除」（本地记录可能尚未同步到服务端）
		ok2 = true
	}
	jsonOK(w, map[string]any{"ok": ok2})
}

// GET /api/debug — 返回当前题库和数据库配置状态（仅供调试）
func (s *Server) handleDebug(w http.ResponseWriter, r *http.Request) {
	uid := getUserID(r)
	ctx := r.Context()
	type bankInfo struct {
		Index         int    `json:"index"`
		Path          string `json:"path"`
		Name          string `json:"name"`
		RecordEnabled bool   `json:"record_enabled"`
		DBNil         bool   `json:"db_nil"`
		PgStoreNil    bool   `json:"pgstore_nil"`
		QuestionCount int    `json:"question_count"`
		Sessions      int    `json:"sessions"`
		Attempts      int    `json:"attempts"`
		WrongAttempts int    `json:"wrong_attempts"`
		WrongTopics   int    `json:"wrong_topics"`
	}
	banks := make([]bankInfo, len(s.cfg.Banks))
	for i, b := range s.cfg.Banks {
		info := bankInfo{
			Index:         i,
			Path:          b.Path,
			Name:          b.bankName(),
			RecordEnabled: b.RecordEnabled,
			DBNil:         b.DB == nil,
			PgStoreNil:    b.PgStore == nil,
			QuestionCount: len(b.Questions),
		}
		if b.PgStore != nil {
			ov := b.PgStore.GetOverallStats(ctx, uid, b.BankID, "")
			info.Sessions      = ov.Sessions
			info.Attempts      = ov.Attempts
			info.WrongAttempts = ov.Wrong
			info.WrongTopics   = len(b.PgStore.GetWrongFingerprints(ctx, uid, b.BankID, 10000))
			banks[i] = info
			// Detailed diagnostic for first bank
			if i == 0 {
				jsonOK(w, map[string]any{
					"uid": uid, "uid_is_legacy": uid == "_legacy" || uid == "",
					"banks": banks,
					"diag": b.PgStore.DiagAttempts(ctx, uid, b.BankID),
				})
				return
			}
		} else if b.DB != nil {
			b.DB.QueryRow("SELECT COUNT(*) FROM sessions WHERE user_id=?", uid).Scan(&info.Sessions)
			b.DB.QueryRow("SELECT COUNT(*) FROM attempts WHERE user_id=?", uid).Scan(&info.Attempts)
			b.DB.QueryRow("SELECT COUNT(*) FROM attempts WHERE user_id=? AND result=0", uid).Scan(&info.WrongAttempts)
			var wt int
			b.DB.QueryRow("SELECT COUNT(DISTINCT fingerprint) FROM attempts WHERE user_id=? AND result=0", uid).Scan(&wt)
			info.WrongTopics = wt
		}
		banks[i] = info
	}
	jsonOK(w, map[string]any{
		"uid":           uid,
		"uid_is_legacy": uid == "_legacy" || uid == "",
		"banks":         banks,
	})
}

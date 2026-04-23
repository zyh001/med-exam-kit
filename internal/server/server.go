package server

import (
	"github.com/zyh001/med-exam-kit/internal/logger"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"math"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/zyh001/med-exam-kit/internal/ai"
	"github.com/zyh001/med-exam-kit/internal/auth"
	"github.com/zyh001/med-exam-kit/internal/bank"
	"github.com/zyh001/med-exam-kit/internal/models"
	"github.com/zyh001/med-exam-kit/internal/progress"
	"github.com/zyh001/med-exam-kit/internal/store"
)

// BankEntry holds one loaded question bank and its associated state.
type BankEntry struct {
	Path          string
	Name          string // display name; overrides Path-derived name
	BankID        int    // PG bank_id (0 for SQLite/legacy)
	Password      string
	Questions     []*models.Question
	DB            *sql.DB  // SQLite progress DB (nil when using PgStore)
	PgStore       PgStorer // PostgreSQL store (nil when using SQLite)
	RecordEnabled bool
}

// shareStorer is an optional interface that PgStorer implementations may satisfy
// to persist share tokens across server restarts (PG mode only).
// JSON []byte is used to bridge the type without creating an import cycle.
type shareStorer interface {
	SaveShareTokenJSON(ctx context.Context, token string, data []byte) error
	LoadShareTokenJSON(ctx context.Context, token string) []byte
	DeleteShareToken(ctx context.Context, token string)
	CleanExpiredShareTokens(ctx context.Context)
}

// favStorer is an optional interface for server-side favorites persistence (PG mode only).
// SQLite mode keeps favorites in localStorage only.
type favStorer interface {
	SyncFavorites(ctx context.Context, userID string, bankID int, adds []store.FavItem, removes [][2]any) ([]store.FavItem, error)
}

// allowing server.go to stay decoupled from the concrete pgstore type.
type PgStorer interface {
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
	if b.Name != "" {
		return b.Name
	}
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
	// AI Q&A
	AIProvider      string
	AIModel         string
	AIAPIKey        string
	AIBaseURL       string
	AIEnableThinking *bool
	AIMaxTokens     int    // 0 = 使用默认值 2048

	// ASR (语音识别)
	ASRAPIKey  string
	ASRModel   string
	ASRBaseURL string

	// S3 图片存储（可选）— 配置后 /api/img/proxy 优先从 S3 重定向
	S3Endpoint   string
	S3Bucket     string
	S3AccessKey  string
	S3SecretKey  string
	S3PublicBase string

	// 数据保留策略
	CleanupDays int // 不活跃用户数据保留天数，0 = 使用默认值 7

	// 调试模式（绝不在生产环境启用）
	// 打开后暴露 /api/debug 与 /api/debug/exam-sessions 等诊断端点，
	// 会泄露内部会话 ID、题库信息等，仅供排障使用。
	Debug bool

	// 可信代理 IP/CIDR 列表。只有来自这些地址的请求才会被信任
	// X-Real-IP / X-Forwarded-For header。默认 loopback (127/8, ::1)
	// 始终可信；其它代理（如同网段 nginx、负载均衡器）需要在这里显式列出。
	// 空列表 + 非 loopback 源 = 忽略所有 forwarded header。
	TrustedProxies []string

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
	aiClient     *ai.Client
	sessionToken string
	assetVer     string
	mux          *http.ServeMux
	rateMu       sync.Mutex
	rateBuckets  map[string][]time.Time
	// Session J: 两个额外的、独立于通用 rateBuckets 的限流桶，
	// 对特别昂贵的端点做更严格的 per-IP 节流。通用 rateBuckets 是 120 req/60s，
	// 对转发 20MB 图片或每次消耗 AI token 的请求还是偏宽。
	imgProxyBuckets map[string][]time.Time // /api/img/proxy + /api/img/local/*
	aiChatBuckets   map[string][]time.Time // /api/ai/chat + /api/ai/report
	// 扫描器封禁：IP → 解封时间（零值表示永久封禁）
	scanBans     map[string]time.Time
	httpServer   *http.Server
	// 热重载
	reloadFn     ReloadFunc // 由 cmd/quiz.go 注入
	configPath   string     // 当前配置文件路径（SIGHUP 时重读）
	// 图标 PNG 缓存（启动时按需生成一次，避免每次请求重复编码）
	iconOnce sync.Once
	icon192  []byte
	icon512  []byte
	// Web Push
	vapidKeys       *VAPIDKeys
	pushStores      map[string]*pushStore // bankPath → store
	pushTestMu      sync.Mutex
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
	answers   map[string]examAnswer // fingerprint → answer+discuss
	ts        int64                 // unix seconds — creation time, used for 24h retention
	startedAt int64                 // unix milliseconds — authoritative exam start time (server clock)
	timeLimit int                   // seconds; 0 = no limit
	// revealedAt 在客户端调用 /api/exam/reveal 成功后设置为 unix 秒。
	// 之后的 revealGraceWindow 秒内再次请求 reveal 会返回同样的答案（幂等）—
	// 这是为了容错：网络抖动、响应中途断开、用户手动 + 自动双触发交卷等都可能
	// 导致客户端第一次拿不到完整答案而需要重试。
	// 宽限窗口过后 session 会被后台清理循环删除。
	revealedAt int64                 // unix seconds, 0 = not yet revealed
}

const revealGraceWindow = 180 // seconds — 交卷答案可重复领取的宽限窗口

type shareConfig struct {
	Fingerprints   []string           `json:"fingerprints"`
	SubIds         []string           `json:"sub_ids"` // "fingerprint:si" pairs for sub-question precision
	Mode           string             `json:"mode"`
	BankIdx        int                `json:"bank_idx"`
	TimeLimit      int                `json:"time_limit"`       // seconds
	Scoring        bool               `json:"scoring"`          // 是否启用计分
	ScorePerMode   map[string]float64 `json:"score_per_mode"`   // 各题型每小题分值
	MultiScoreMode string             `json:"multi_score_mode"` // strict|loose
	Ts             int64              `json:"ts"`
	ExpiresAt      int64              `json:"expires_at"`
}

const (
	rateLimit  = 120
	rateWindow = 60 * time.Second
	// Session J：两个特殊端点的限流参数。窗口内请求数用滑动时间窗口统计。
	// 10 次 / 10 秒 对正常用户足够（做题页面一屏常见 5~10 张题目图片），
	// 但可以挡住恶意批量打 img-proxy 当免费 CDN / 外链流量放大器的行为。
	imgProxyRateLimit  = 10
	imgProxyRateWindow = 10 * time.Second
	// AI chat 每次都消耗成本（LLM token），给 10 req/min 比较保守。
	aiChatRateLimit  = 10
	aiChatRateWindow = 60 * time.Second
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
		cfg:             cfg,
		sessionToken:    hex.EncodeToString(tok),
		assetVer:        hex.EncodeToString(ver),
		mux:             http.NewServeMux(),
		rateBuckets:     map[string][]time.Time{},
		imgProxyBuckets: map[string][]time.Time{},
		aiChatBuckets:   map[string][]time.Time{},
		scanBans:        map[string]time.Time{},
	}
	// 初始化 Web Push
	pushStores := map[string]*pushStore{}
	pushTestBuckets := map[string][]time.Time{}
	for _, b := range cfg.Banks {
		pushStores[b.Path] = newPushStore()
	}
	s.pushStores = pushStores
	s.pushTestBuckets = pushTestBuckets
	s.examSessions = map[string]*examSession{}
	s.shareTokens = map[string]*shareConfig{}
	if keys, err := generateVAPIDKeys(); err == nil {
		s.vapidKeys = keys
	} else {
		logger.Errorf("[push] VAPID 密钥生成失败: %v", err)
	}

	// Initialize AI client if API key is configured
	if cfg.AIAPIKey != "" || cfg.AIProvider == "ollama" {
		s.aiClient = ai.NewClient(cfg.AIProvider, cfg.AIAPIKey, cfg.AIBaseURL, cfg.AIModel, 120, cfg.AIEnableThinking)
		logger.Infof("[ai] AI 答疑已启用: provider=%s model=%s", cfg.AIProvider, s.aiClient.Model)
	}

	// Log S3 configuration status
	if cfg.S3Endpoint != "" && cfg.S3Bucket != "" && cfg.S3AccessKey != "" {
		logger.Infof("[s3] 图片存储已启用: endpoint=%s bucket=%s", cfg.S3Endpoint, cfg.S3Bucket)
	} else {
		logger.Debugf("[s3] 未配置 S3，图片将直接代理外链")
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
		WriteTimeout: 0, // disabled — SSE streams are long-lived; nginx enforces upstream timeouts
		IdleTimeout:  120 * time.Second,
	}

	// 优雅关闭：监听信号
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	// SIGHUP：热重载配置（类似 nginx -s reload）
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		for range hup {
			logger.Infof("[reload] 收到 SIGHUP，开始热重载题库...")
			if err := s.HotReload(nil, ""); err != nil {
				logger.Errorf("[reload] 热重载失败: %v", err)
			} else {
				logger.Infof("[reload] 热重载完成")
			}
		}
	}()

	errCh := make(chan error, 1)
	go func() {
		logger.Infof("Server listening on %s", addr)
		if err := s.httpServer.ListenAndServe(); err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	// 定期清理脏数据：删除 7 天未活跃用户的答题记录
	go func() {
		// 首次启动 1 分钟后执行一次，之后每 24 小时执行
		time.Sleep(1 * time.Minute)
		s.runUserCleanup()
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			s.runUserCleanup()
		}
	}()

	select {
	case err := <-errCh:
		return err
	case sig := <-stop:
		logger.Infof("Received signal %v, shutting down...", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.httpServer.Shutdown(ctx); err != nil {
			logger.Errorf("Shutdown error: %v", err)
		}
		s.Close()
		logger.Infof("Server stopped")
	}
	return nil
}

// SetReloadFunc registers the function used for hot-reload.
func (s *Server) SetReloadFunc(fn ReloadFunc) { s.reloadFn = fn }

// SetConfigPath records the config file path for SIGHUP reloads.
func (s *Server) SetConfigPath(p string) { s.configPath = p }

// Close closes all database connections.
func (s *Server) Close() {
	for _, b := range s.cfg.Banks {
		if b.DB != nil {
			b.DB.Close()
		}
	}
}

// runUserCleanup removes stale user data from all SQLite banks,
// and purges idle IP entries from the rate-limit map.
func (s *Server) runUserCleanup() {
	days := s.cfg.CleanupDays
	if days <= 0 {
		days = 7
	}
	for i, b := range s.cfg.Banks {
		if b.DB == nil {
			continue
		}
		users, rows := progress.CleanupStaleUsers(b.DB, days)
		if users > 0 {
			logger.Warnf("[cleanup] bank=%d: removed %d stale users (%d rows) [threshold=%dd]", i, users, rows, days)
		}
	}
	// 清理 rateBuckets：删除窗口期内无任何请求的 IP 条目，防止长期运行内存无限增长
	s.rateMu.Lock()
	cleanBucket := func(buckets map[string][]time.Time, window time.Duration) {
		cutoff := time.Now().Add(-window)
		for ip, bucket := range buckets {
			hasRecent := false
			for _, t := range bucket {
				if t.After(cutoff) {
					hasRecent = true
					break
				}
			}
			if !hasRecent {
				delete(buckets, ip)
			}
		}
	}
	cleanBucket(s.rateBuckets, rateWindow)
	cleanBucket(s.imgProxyBuckets, imgProxyRateWindow)
	cleanBucket(s.aiChatBuckets, aiChatRateWindow)
	// 清理已过期的扫描器封禁条目
	now := time.Now()
	for ip, until := range s.scanBans {
		if !until.IsZero() && now.After(until) {
			delete(s.scanBans, ip)
		}
	}
	s.rateMu.Unlock()
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	wrapped := &responseWriterWrapper{ResponseWriter: w, statusCode: 200}
	defer func() {
		logRequest(r, wrapped.statusCode, time.Since(start))
	}()

	auth.ApplySecurityHeaders(wrapped)

	ip := s.remoteIP(r)

	// ── 扫描器检测：命中可疑路径立即封禁 24h ──────────────────────
	if s.isScannerPath(r.URL.Path, r.Method) {
		s.banIP(ip, 24*time.Hour)
		logger.Warnf("[ban] scanner detected ip=%s path=%s ua=%s", ip, r.URL.Path, r.UserAgent())
		wrapped.WriteHeader(http.StatusNotFound)
		return
	}

	// ── 封禁检查：已封禁 IP 直接丢弃 ──────────────────────────────
	if s.isBanned(ip) {
		wrapped.WriteHeader(http.StatusForbidden)
		return
	}

	// 健康检查端点不需要认证
	if r.URL.Path == "/api/health" {
		s.mux.ServeHTTP(wrapped, r)
		return
	}

	if r.URL.Path == "/auth" && r.Method == http.MethodPost {
		if s.cfg.AccessCode != "" {
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
			if auth.NeedsCaptcha(ip) {
				tok, svg = auth.NewCaptcha()
			}
			io.WriteString(wrapped, auth.RenderPINPage("医考练习", "", s.cfg.PinLen, tok, svg))
			return
		}
		jsonError(wrapped, "Unauthorized", http.StatusUnauthorized)
		return
	}
	// /api/debug 只需访问码，无需 Session Token（调试用，不含敏感操作）
	// /api/img/proxy 由 <img> 标签直接请求，无法携带自定义 Header，豁免 Token 校验
	// /api/img/local/ 同理：编辑器/做题页面用 <img> 加载私有 S3 图片，无法携带 Header；
	//   安全保障来自第一层 Cookie 访问码验证 + UUID 随机文件名（不可猜测）
	if strings.HasPrefix(r.URL.Path, "/api/") &&
		r.URL.Path != "/api/debug" &&
		r.URL.Path != "/api/img/proxy" &&
		!strings.HasPrefix(r.URL.Path, "/api/img/local/") {
		tok := r.Header.Get("X-Session-Token")
		// WebSocket 无法设置自定义 Header，从 query param 获取 token
		if tok == "" {
			tok = r.URL.Query().Get("token")
		}
		if subtle.ConstantTimeCompare([]byte(tok), []byte(s.sessionToken)) != 1 {
			jsonError(wrapped, "Unauthorized", http.StatusUnauthorized)
			return
		}
		if !s.checkRate(ip) {
			jsonError(wrapped, "Too Many Requests", http.StatusTooManyRequests)
			return
		}
	}
	// Session J：对两个昂贵端点做额外的更严格限流（与通用桶叠加）。
	// 注意这里放在通用 token + rate 检查之后，但必须覆盖所有能访问这些端点的
	// 请求路径 —— img-proxy / img-local 豁免了 token 校验，所以上面 if 分支里
	// 的 checkRate 不会对它们起作用；在这里补一道。
	if r.URL.Path == "/api/img/proxy" ||
		strings.HasPrefix(r.URL.Path, "/api/img/local/") {
		if !s.checkImgProxyRate(ip) {
			jsonError(wrapped, "图片请求过于频繁，请稍后再试", http.StatusTooManyRequests)
			return
		}
	}
	if r.URL.Path == "/api/ai/chat" || r.URL.Path == "/api/ai/report" {
		if !s.checkAIChatRate(ip) {
			jsonError(wrapped, "AI 请求过于频繁，请稍后再试", http.StatusTooManyRequests)
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
	// Hot-reload endpoint (POST /api/admin/reload)
	m.HandleFunc("POST /api/admin/reload", s.handleAdminReload)
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
	m.HandleFunc("GET /api/debug/exam-sessions", s.handleDebugExamSessions)
	m.HandleFunc("GET /api/sync/status", s.handleSyncStatus)
	m.HandleFunc("GET /api/exam/reveal", s.handleExamReveal)
	m.HandleFunc("GET /api/exam/time", s.handleExamTime)
	m.HandleFunc("POST /api/exam/share", s.handleExamShare)
	m.HandleFunc("GET /api/exam/join", s.handleExamJoin)
	// Favorites sync (PG mode only; SQLite falls back gracefully)
	m.HandleFunc("POST /api/favorites/sync", s.handleFavSync)
	// AI Q&A
	m.HandleFunc("POST /api/ai/chat",   s.handleAIChat)
	m.HandleFunc("POST /api/ai/report", s.handleAIReport)
	m.HandleFunc("GET /api/asr/ws", s.handleASRWebSocket)
	// Image proxy (cross-origin fix)
	m.HandleFunc("GET /api/img/proxy", s.handleImgProxy)
	// Image upload to S3 + private serve (editor use)
	m.HandleFunc("POST /api/img/upload",    s.handleImgUpload)
	m.HandleFunc("GET /api/img/local/",     s.handleImgLocal)
}

// ── Bank selection ────────────────────────────────────────────────────

// bankForReq returns the BankEntry for the request's ?bank=N param.
// Uses a read lock so hot-reload can safely swap s.cfg.Banks concurrently.
func (s *Server) bankForReq(r *http.Request) (*BankEntry, int, bool) {
	banksMu.RLock()
	defer banksMu.RUnlock()
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
	b := s.cfg.Banks[idx] // copy, not pointer into slice
	return &b, idx, true
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
			key := fmt.Sprintf("%s:%d", rows[i].Fingerprint, rows[i].SI)
			answers[key] = examAnswer{Answer: rows[i].Answer, Discuss: rows[i].Discuss}
			rows[i].Answer = ""
			rows[i].Discuss = ""
			rows[i].Unit = "" // 考试模式隐藏章节信息，防止泄露提示
		}
		// 计时使用服务器时间作为权威来源：防止客户端篡改系统时间作弊，
		// 也避免浏览器在后台被挂起导致 setInterval 漂移；客户端以服务端
		// started_at + time_limit 为准逐秒计算 remaining。
		timeLimitSec, _ := strconv.Atoi(q.Get("time_limit"))
		if timeLimitSec < 0 {
			timeLimitSec = 0
		}
		nowMS := time.Now().UnixMilli()
		s.examMu.Lock()
		// 清理超过 24h 的旧 session（简单 LRU）；
		// 已 reveal 的 session 在宽限窗口结束后也一并清理，避免常驻内存。
		nowS := time.Now().Unix()
		for k, v := range s.examSessions {
			if nowS-v.ts > 86400 ||
				(v.revealedAt != 0 && nowS-v.revealedAt > int64(revealGraceWindow)) {
				delete(s.examSessions, k)
			}
		}
		s.examSessions[eid] = &examSession{
			answers:   answers,
			ts:        nowS,
			startedAt: nowMS,
			timeLimit: timeLimitSec,
		}
		s.examMu.Unlock()
		jsonOK(w, map[string]any{
			"total":      len(rows),
			"items":      rows,
			"exam_id":    eid,
			"started_at": nowMS,
			"server_now": nowMS,
			"time_limit": timeLimitSec,
		})
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
	if err := decodeJSONBody(w, r, &data); err != nil {
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
	if err := decodeJSONBody(w, r, &data); err != nil {
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
	if err := decodeJSONBody(w, r, &data); err != nil {
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
	if err := decodeJSONBody(w, r, &data); err != nil {
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
	if err := decodeJSONBody(w, r, &data); err != nil || data == nil {
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
		jsonOK(w, map[string]any{"ok": true, "deleted": map[string]int{"attempts": 0, "sessions": 0, "sm2_cards": 0}})
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
		if ov.Attempts > 0 {
			accuracy = int(math.Round(float64(ov.Correct) / float64(ov.Attempts) * 100))
		}
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
	for i := range b.Questions {
		bankFPs = append(bankFPs, b.Questions[i].Fingerprint)
	}
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
	for i := range b.Questions {
		bankFPs2 = append(bankFPs2, b.Questions[i].Fingerprint)
	}
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
		for i := range b.Questions {
			wbFPs = append(wbFPs, b.Questions[i].Fingerprint)
		}
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
	decodeJSONBody(w, r, &payload)
	uid := getUserID(r)

	// sessions 中既不在 processed 也不在 skipped 的 → 写入失败，让客户端重试
	computeFailed := func(sessions []map[string]any, done, skipped []string) []map[string]any {
		okSet := make(map[string]bool, len(done)+len(skipped))
		for _, id := range done {
			okSet[id] = true
		}
		for _, id := range skipped {
			okSet[id] = true
		}
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
			if payload.Sessions[i] == nil {
				payload.Sessions[i] = map[string]any{}
			}
			payload.Sessions[i]["bank_id"] = b.BankID
		}
		done, skipped := b.PgStore.RecordSessionsBatch(r.Context(), payload.Sessions, uid)
		failed := computeFailed(payload.Sessions, done, skipped)
		if len(failed) > 0 {
			logger.Warnf("[sync] PG uid=%s bank=%d processed=%d skipped=%d failed=%d",
				uid, b.BankID, len(done), len(skipped), len(failed))
		}
		jsonOK(w, map[string]any{"ok": true, "processed": done, "skipped": skipped, "failed": failed})
		return
	}
	// SQLite 模式
	if b.DB == nil {
		logger.Warnf("[sync] WARN: b.DB is nil, RecordEnabled=%v", b.RecordEnabled)
		jsonOK(w, map[string]any{"ok": false, "error": "DB not initialised"})
		return
	}
	for i := range payload.Sessions {
		if payload.Sessions[i] == nil {
			payload.Sessions[i] = map[string]any{}
		}
		payload.Sessions[i]["bank_id"] = b.BankID
	}
	logger.Debugf("[sync] uid=%s bank=%d sessions=%d", uid, b.BankID, len(payload.Sessions))
	done, skipped := progress.RecordSessionsBatch(b.DB, payload.Sessions, uid)
	failed := computeFailed(payload.Sessions, done, skipped)
	logger.Debugf("[sync] uid=%s done=%d skipped=%d failed=%d", uid, len(done), len(skipped), len(failed))
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
	nowS := time.Now().Unix()
	s.examMu.Lock()
	sess, ok := s.examSessions[eid]
	var answers map[string]examAnswer
	if ok {
		// 幂等领取：第一次调用时记录 revealedAt，之后 revealGraceWindow 秒内
		// 再次调用返回同样的答案。这样手动交卷和定时自动交卷的竞态、响应中
		// 断导致的重试、前端 submitExam 被意外双触发等情况都不会丢答案。
		if sess.revealedAt == 0 {
			sess.revealedAt = nowS
		} else if nowS-sess.revealedAt > int64(revealGraceWindow) {
			// 宽限期已过，视同过期
			delete(s.examSessions, eid)
			ok = false
		}
		if ok {
			answers = sess.answers
		}
	}
	s.examMu.Unlock()
	if !ok {
		jsonError(w, "考试会话已过期或不存在", http.StatusNotFound)
		return
	}
	jsonOK(w, map[string]any{"answers": answers})
}

// handleExamTime 返回考试剩余时间（以服务端时钟为准）
//
// 设计：考试模式的计时不再信任客户端的 Date.now()——用户可能修改系统
// 时间作弊，浏览器也可能在后台标签页限制 setInterval 精度导致计时漂移。
// 客户端在考试开始时，以及每次从后台切回前台时调用此接口，得到：
//
//	server_now: 服务端当前 unix 毫秒
//	started_at: 服务端记录的考试起始 unix 毫秒
//	time_limit: 考试总时长（秒，0 表示无限）
//	remaining:  服务端计算好的剩余秒数（不会小于 0）
//
// 客户端据此更新本地 clock offset 与显示，逐秒用 wall-clock 计算剩余时间。
// 与 reveal 不同，本端点不会吞掉 session，可以反复调用。
func (s *Server) handleExamTime(w http.ResponseWriter, r *http.Request) {
	eid := r.URL.Query().Get("id")
	if eid == "" {
		jsonError(w, "缺少 exam id", http.StatusBadRequest)
		return
	}
	s.examMu.Lock()
	sess, ok := s.examSessions[eid]
	var startedAt int64
	var timeLimit int
	if ok {
		startedAt = sess.startedAt
		timeLimit = sess.timeLimit
	}
	s.examMu.Unlock()
	if !ok {
		jsonError(w, "考试会话已过期或不存在", http.StatusNotFound)
		return
	}
	nowMS := time.Now().UnixMilli()
	remaining := 0
	if timeLimit > 0 {
		elapsed := (nowMS - startedAt) / 1000
		remaining = timeLimit - int(elapsed)
		if remaining < 0 {
			remaining = 0
		}
	}
	jsonOK(w, map[string]any{
		"server_now": nowMS,
		"started_at": startedAt,
		"time_limit": timeLimit,
		"remaining":  remaining,
	})
}

// handleExamShare 生成试卷分享令牌
func (s *Server) handleExamShare(w http.ResponseWriter, r *http.Request) {
	_, bankIdx, ok := s.bankForReq(r)
	if !ok {
		jsonError(w, "bank not found", http.StatusNotFound)
		return
	}
	var body struct {
		Fingerprints   []string           `json:"fingerprints"`
		SubIds         []string           `json:"sub_ids"`
		Mode           string             `json:"mode"`
		TimeLimit      int                `json:"time_limit"`
		Scoring        bool               `json:"scoring"`
		ScorePerMode   map[string]float64 `json:"score_per_mode"`
		MultiScoreMode string             `json:"multi_score_mode"`
	}
	if err := decodeJSONBody(w, r, &body); err != nil || len(body.Fingerprints) == 0 {
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

	// Session J 加固：8 字节 token (64 bit) 熵不足，理论上可能被枚举。
	// 提升到 16 字节 (128 bit)，对齐业界标准（NIST SP 800-63B）。
	// 已存在的旧短 token 仍然可以被消费，因为查找只按字符串匹配。
	tok := make([]byte, 16)
	rand.Read(tok)
	token := hex.EncodeToString(tok)

	now := time.Now().Unix()
	expiresAt := now + int64(7*24*3600)

	cfg := &shareConfig{
		Fingerprints:   body.Fingerprints,
		SubIds:         body.SubIds,
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
	for _, fp := range cfg.Fingerprints {
		fpSet[fp] = struct{}{}
	}
	rows, _ := selectQuestions(b.Questions, selectOpts{fpSet: fpSet})
	if rows == nil {
		rows = []sqFlat{}
	}

	// 精确过滤到小题级别：若分享时提供了 sub_ids（"fingerprint:si" 对），
	// 只保留接收端明确指定的那些小题，防止服务端自动把同一题干下所有小题
	// 全部还原（导致 220 → 221 等多出题目的情况）。
	if len(cfg.SubIds) > 0 {
		allowed := make(map[string]struct{}, len(cfg.SubIds))
		for _, id := range cfg.SubIds {
			allowed[id] = struct{}{}
		}
		filtered := make([]sqFlat, 0, len(rows))
		for _, row := range rows {
			key := row.Fingerprint + ":" + strconv.Itoa(row.SI)
			if _, ok := allowed[key]; ok {
				filtered = append(filtered, row)
			}
		}
		rows = filtered
	}

	// sealed 模式：剥离答案，服务端暂存。
	// exam_done 是前端交卷后的内部状态，功能上等同于 exam，同样需要密封答案。
	// 返回给客户端时统一使用 "exam"，让接收方直接进入考试模式。
	var eid string
	isExamMode := cfg.Mode == "exam" || cfg.Mode == "exam_done"
	if isExamMode && len(rows) > 0 {
		eidBytes := make([]byte, 16)
		rand.Read(eidBytes)
		eid = hex.EncodeToString(eidBytes)
		answers := make(map[string]examAnswer, len(rows))
		for i := range rows {
			key := fmt.Sprintf("%s:%d", rows[i].Fingerprint, rows[i].SI)
			answers[key] = examAnswer{Answer: rows[i].Answer, Discuss: rows[i].Discuss}
			rows[i].Answer = ""
			rows[i].Discuss = ""
		}
		// 计算 timeLimit（移到这里，以便与 session 一起存储）
		tl := cfg.TimeLimit
		if tl <= 0 {
			tl = 90 * 60
		}
		s.examMu.Lock()
		// 清理：超过 24h 的旧 session，或已 reveal 且超出宽限窗口的 session
		for k, v := range s.examSessions {
			if now-v.ts > 86400 ||
				(v.revealedAt != 0 && now-v.revealedAt > int64(revealGraceWindow)) {
				delete(s.examSessions, k)
			}
		}
		// 分享考试也必须设置 startedAt / timeLimit，否则 /api/exam/time 会返回 remaining=0
		// 导致客户端一进入考试就认为已到期；而 reveal 则可能因为 ts 不一致导致答案
		// 无法回填（q.answer 为空，calculateResults 会把所有题判错）。
		s.examSessions[eid] = &examSession{
			answers:   answers,
			ts:        now,
			startedAt: time.Now().UnixMilli(),
			timeLimit: tl,
		}
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

	// 与 handleQuestions 的 sealed 分支保持一致：把 started_at / server_now 一并下发，
	// 客户端据此校准 clock offset、避免使用本地 Date.now() 被系统时间篡改。
	nowMS := time.Now().UnixMilli()
	var startedAt int64
	if eid != "" {
		// sealed 模式下 startedAt 已存入 session，取同一个值给客户端
		s.examMu.Lock()
		if sess, ok := s.examSessions[eid]; ok {
			startedAt = sess.startedAt
		}
		s.examMu.Unlock()
	}

	jsonOK(w, map[string]any{
		"items": rows, "total": len(rows), "mode": outMode,
		"exam_id": eid, "time_limit": timeLimit,
		"started_at":       startedAt,
		"server_now":       nowMS,
		"bank_idx":         cfg.BankIdx,
		"scoring":          cfg.Scoring,
		"score_per_mode":   cfg.ScorePerMode,
		"multi_score_mode": cfg.MultiScoreMode,
	})
}

// ── API: favorites sync ───────────────────────────────────────────
//
// POST /api/favorites/sync?bank=N
//
//	Body:    { "adds": [{"fp":"...","si":0,"ts":1234567890}], "removes": [{"fp":"...","si":0}] }
//	Returns: { "ok": true, "items": [{"fp":"...","si":0,"added_at":1234567890}] }
//
// Single endpoint handles both upload (adds/removes) and download (full server list).
// SQLite mode: returns ok:true with empty items so the frontend degrades gracefully.
func (s *Server) handleFavSync(w http.ResponseWriter, r *http.Request) {
	b, _, ok := s.bankForReq(r)
	if !ok {
		jsonError(w, "bank not found", http.StatusNotFound)
		return
	}

	// SQLite 模式：收藏仅本地存储，返回空列表让前端降级
	fs, hasFavStore := b.PgStore.(favStorer)
	if !hasFavStore {
		jsonOK(w, map[string]any{"ok": true, "items": []any{}})
		return
	}

	var body struct {
		Adds    []struct {
			FP string  `json:"fp"`
			SI float64 `json:"si"`
			Ts int64   `json:"ts"`
		} `json:"adds"`
		Removes []struct {
			FP string  `json:"fp"`
			SI float64 `json:"si"`
		} `json:"removes"`
	}
	if err := decodeJSONBody(w, r, &body); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}

	uid := getUserID(r)

	adds := make([]store.FavItem, 0, len(body.Adds))
	for _, a := range body.Adds {
		if a.FP == "" {
			continue
		}
		adds = append(adds, store.FavItem{
			Fingerprint: a.FP,
			SI:          int(a.SI),
			AddedAt:     a.Ts,
		})
	}
	removes := make([][2]any, 0, len(body.Removes))
	for _, rm := range body.Removes {
		if rm.FP == "" {
			continue
		}
		removes = append(removes, [2]any{rm.FP, rm.SI})
	}

	items, err := fs.SyncFavorites(r.Context(), uid, b.BankID, adds, removes)
	if err != nil {
		logger.Errorf("[fav] SyncFavorites uid=%s bank=%d err=%v", uid, b.BankID, err)
		jsonError(w, "sync failed", http.StatusInternalServerError)
		return
	}
	if items == nil {
		items = []store.FavItem{}
	}
	jsonOK(w, map[string]any{"ok": true, "items": items})
}

// ── AI Q&A ────────────────────────────────────────────────────────────────
func (s *Server) handleAIChat(w http.ResponseWriter, r *http.Request) {
	if s.aiClient == nil {
		jsonError(w, "AI 功能未配置", http.StatusServiceUnavailable)
		return
	}

	var body struct {
		Fingerprint string           `json:"fingerprint"`
		SQIndex     int              `json:"sq_index"`
		UserAnswer  string           `json:"user_answer"`
		Bank        int              `json:"bank"`
		History     []ai.ChatMessage `json:"history"`
	}
	if err := decodeJSONBody(w, r, &body); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}

	// Look up question by fingerprint across all banks
	var question *models.Question
	bankIdx := body.Bank
	if bankIdx < 0 || bankIdx >= len(s.cfg.Banks) {
		bankIdx = 0
	}
	for _, b := range s.cfg.Banks {
		for _, q := range b.Questions {
			if q.Fingerprint == body.Fingerprint {
				question = q
				break
			}
		}
		if question != nil {
			break
		}
	}
	if question == nil {
		jsonError(w, "题目未找到", http.StatusNotFound)
		return
	}
	if body.SQIndex < 0 || body.SQIndex >= len(question.SubQuestions) {
		jsonError(w, "小题索引无效", http.StatusBadRequest)
		return
	}

	// Build messages: system prompt + conversation history
	messages := ai.BuildAIChatPrompt(question, body.SQIndex, body.UserAnswer)

	// If this is a follow-up (history has prior messages), append them
	if len(body.History) > 0 {
		messages = append(messages, body.History...)
	}

	// Start streaming
	maxTokens := s.cfg.AIMaxTokens
	if maxTokens <= 0 {
		maxTokens = 2048
	}
	ch, err := s.aiClient.ChatCompletionStream(r.Context(), messages, 0.7, maxTokens)
	if err != nil {
		jsonError(w, fmt.Sprintf("AI 请求失败: %v", err), http.StatusInternalServerError)
		return
	}

	// SSE response
	flusher, ok := w.(http.Flusher)
	if !ok {
		jsonError(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	flusher.Flush()

	// Heartbeat: send SSE comment every 15s to keep connection alive
	// (prevents nginx proxy_read_timeout from closing the connection)
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-heartbeat.C:
			fmt.Fprintf(w, ": heartbeat\n\n")
			flusher.Flush()
		case chunk, ok := <-ch:
			if !ok {
				return // channel closed
			}
			if chunk.Err != nil {
				data, _ := json.Marshal(map[string]string{"error": chunk.Err.Error()})
				fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
				return
			}
			if chunk.Done {
				if chunk.Truncated {
					// 通知前端：输出被 max_tokens 截断，可让用户点击"继续"
					td, _ := json.Marshal(map[string]any{"truncated": true})
					fmt.Fprintf(w, "data: %s\n\n", td)
					flusher.Flush()
				}
				fmt.Fprintf(w, "data: [DONE]\n\n")
				flusher.Flush()
				return
			}
			data, _ := json.Marshal(map[string]string{"content": chunk.Content, "reasoning": chunk.ReasoningContent})
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// POST /api/ai/report — 根据考试结果生成流式 AI 分析报告
func (s *Server) handleAIReport(w http.ResponseWriter, r *http.Request) {
	if s.aiClient == nil {
		jsonError(w, "AI 功能未配置", http.StatusServiceUnavailable)
		return
	}

	var body struct {
		Total    int     `json:"total"`
		Correct  int     `json:"correct"`
		Wrong    int     `json:"wrong"`
		Skip     int     `json:"skip"`
		TimeSec  int     `json:"time_sec"`
		Score    float64 `json:"score"`
		MaxScore float64 `json:"max_score"`
		ByUnit   []struct {
			Unit    string `json:"unit"`
			Correct int    `json:"correct"`
			Total   int    `json:"total"`
		} `json:"by_unit"` // 全部章节，按正确率升序
		ByMode []struct {
			Mode    string `json:"mode"`
			Correct int    `json:"correct"`
			Total   int    `json:"total"`
		} `json:"by_mode"` // 全部题型统计
		WrongStat []struct {
			Unit    string `json:"unit"`
			Mode    string `json:"mode"`
			Answer  string `json:"answer"`
			UserAns string `json:"user_ans"`
		} `json:"wrong_stat"` // 全部错题（无题干），用于统计分析
		WrongSample []struct {
			Unit    string `json:"unit"`
			Mode    string `json:"mode"`
			Text    string `json:"text"`
			Answer  string `json:"answer"`
			UserAns string `json:"user_ans"`
		} `json:"wrong_sample"` // 前20条带题干，供AI识别错误规律
	}
	if err := decodeJSONBody(w, r, &body); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}

	pct := 0
	if body.Total > 0 {
		pct = body.Correct * 100 / body.Total
	}

	// ── 章节统计（全量，按正确率升序）──
	unitSummary := fmt.Sprintf("共 %d 个章节，按正确率由低到高：\n", len(body.ByUnit))
	for _, u := range body.ByUnit {
		rate := 0
		if u.Total > 0 {
			rate = u.Correct * 100 / u.Total
		}
		unitSummary += fmt.Sprintf("  - %s：%d/%d 正确（%d%%）\n", u.Unit, u.Correct, u.Total, rate)
	}
	if len(body.ByUnit) == 0 {
		unitSummary = "  （无章节数据）\n"
	}

	// ── 题型统计（全量）──
	modeSummary := ""
	for _, m := range body.ByMode {
		rate := 0
		if m.Total > 0 {
			rate = m.Correct * 100 / m.Total
		}
		modeSummary += fmt.Sprintf("  - %s：%d/%d 正确（%d%%）\n", m.Mode, m.Correct, m.Total, rate)
	}
	if modeSummary == "" {
		modeSummary = "  （无题型数据）\n"
	}

	// ── 错误模式分析（全量错题，无题干）──
	// 统计「错成哪个答案」的频率，帮助 AI 识别规律性混淆
	type wrongKey struct{ Unit, Mode, Answer, UserAns string }
	wrongFreq := map[wrongKey]int{}
	for _, w := range body.WrongStat {
		wrongFreq[wrongKey{w.Unit, w.Mode, w.Answer, w.UserAns}]++
	}
	// 按频率降序，取前 20 个高频错误
	type wf struct {
		wrongKey
		Cnt int
	}
	wfList := make([]wf, 0, len(wrongFreq))
	for k, v := range wrongFreq {
		wfList = append(wfList, wf{k, v})
	}
	sort.Slice(wfList, func(i, j int) bool { return wfList[i].Cnt > wfList[j].Cnt })
	if len(wfList) > 20 {
		wfList = wfList[:20]
	}
	errorPattern := fmt.Sprintf("共 %d 道错题，高频错误（相同题型/章节/答案组合）：\n", len(body.WrongStat))
	for _, w := range wfList {
		freqStr := ""
		if w.Cnt > 1 {
			freqStr = fmt.Sprintf("×%d", w.Cnt)
		}
		errorPattern += fmt.Sprintf("  - [%s/%s] 正确:%s 选了:%s %s\n",
			w.Unit, w.Mode, w.Answer, w.UserAns, freqStr)
	}

	// ── 带题干的错题样本（前 20 条，供 AI 理解题目特征）──
	wrongSample := ""
	for i, w := range body.WrongSample {
		wrongSample += fmt.Sprintf("  %d. [%s/%s] %s → 正确:%s 选了:%s\n",
			i+1, w.Unit, w.Mode, truncateStr(w.Text, 50), w.Answer, w.UserAns)
	}
	if wrongSample == "" {
		wrongSample = "  （全部答对！）\n"
	}

	scoreStr := ""
	if body.MaxScore > 0 {
		scoreStr = fmt.Sprintf("得分：%.1f / %.1f 分\n", body.Score, body.MaxScore)
	}

	prompt := fmt.Sprintf(`你是一位经验丰富的医学考试辅导老师。以下是学生一次模拟考试的完整数据，请据此生成详细的考试分析报告。

## 考试概况
总题数：%d 题 | 答对：%d 题（正确率 %d%%）| 答错：%d 题 | 未答：%d 题 | 用时：%s
%s
## 各章节正确率（全量，按正确率由低到高）
%s
## 各题型正确率
%s
## 错误规律分析数据
%s
## 代表性错题样本（前 %d 条，含题干）
%s
## 分析报告要求

请用 Markdown 格式输出，结构如下：

### 📊 总体评价
对整体表现做简明评价，结合正确率和用时给出定性判断。

### 🔍 薄弱章节分析
针对正确率低于 60%% 的章节，逐章分析可能的知识薄弱点，结合错题规律数据指出是概念性错误还是临床应用不熟。

### 🔄 题型表现分析
分析各题型的正确率差异，若某题型明显偏低，分析原因（如 A3/A4 序贯题逻辑推理、案例分析综合判断等）。

### ❌ 高频错误模式
基于错误规律数据，归纳重复出现的「错误替换模式」（如总把答案 A 错选为 B），推断可能的知识混淆点。

### 📚 针对性复习建议
按优先级给出 3-5 条可执行的复习建议，每条要具体到章节或知识点。

### 💪 下一步学习计划
建议接下来 1-2 周的具体学习安排，包括每天大概用多少时间、复习顺序。

请用鼓励且专业的语气，分析必须结合以上具体数据，不要泛泛而谈。`,
		body.Total, body.Correct, pct, body.Wrong, body.Skip,
		fmtDuration(body.TimeSec), scoreStr,
		unitSummary, modeSummary,
		errorPattern,
		len(body.WrongSample), wrongSample,
	)

	messages := []ai.ChatMessage{
		{Role: "user", Content: prompt},
	}

	maxTokens := s.cfg.AIMaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096 // 报告需要更多 token
	}
	ch, err := s.aiClient.ChatCompletionStream(r.Context(), messages, 0.7, maxTokens)
	if err != nil {
		jsonError(w, fmt.Sprintf("AI 请求失败: %v", err), http.StatusInternalServerError)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		jsonError(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	flusher.Flush()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-heartbeat.C:
			fmt.Fprintf(w, ": heartbeat\n\n")
			flusher.Flush()
		case chunk, ok := <-ch:
			if !ok {
				return
			}
			if chunk.Err != nil {
				data, _ := json.Marshal(map[string]string{"error": chunk.Err.Error()})
				fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
				return
			}
			if chunk.Done {
				if chunk.Truncated {
					td, _ := json.Marshal(map[string]any{"truncated": true})
					fmt.Fprintf(w, "data: %s\n\n", td)
					flusher.Flush()
				}
				fmt.Fprintf(w, "data: [DONE]\n\n")
				flusher.Flush()
				return
			}
			data, _ := json.Marshal(map[string]string{"content": chunk.Content})
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// sortModeOrder 按医学考试标准题型顺序对 modeOrder 原地排序：
// A1 → A1/A2 → A2 → A3/A4 → B → 案例分析 → X型/不定项 → 其他
// 支持"A1/A2型"、"A3/A4型"等合并写法
func sortModeOrder(modes []string) {
	rank := func(m string) int {
		mu := strings.ToUpper(m)
		// 先处理常见合并写法
		switch {
		case strings.Contains(mu, "A1") && strings.Contains(mu, "A2"):
			return 2 // A1/A2合并型，排A1之后A2同位
		case strings.Contains(mu, "A3") && strings.Contains(mu, "A4"):
			return 3 // A3/A4合并型
		case strings.HasPrefix(mu, "A1"):
			return 1
		case strings.HasPrefix(mu, "A2"):
			return 2
		case strings.HasPrefix(mu, "A3"):
			return 3
		case strings.HasPrefix(mu, "A4"):
			return 3
		case strings.HasPrefix(mu, "B"):
			return 4
		case strings.Contains(mu, "案例"):
			return 5
		case strings.Contains(mu, "X型"):
			return 6
		case strings.Contains(mu, "不定项"):
			return 6
		default:
			return 7
		}
	}
	sort.SliceStable(modes, func(i, j int) bool {
		ri, rj := rank(modes[i]), rank(modes[j])
		if ri != rj {
			return ri < rj
		}
		return modes[i] < modes[j]
	})
}

// truncateStr 截断字符串到指定字符数
func truncateStr(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}

// fmtDuration 将秒数格式化为 hh:mm:ss 或 mm:ss
func fmtDuration(sec int) string {
	h := sec / 3600
	m := (sec % 3600) / 60
	s := sec % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}
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
	if err := decodeJSONBody(w, r, &body); err != nil || body.FromUID == "" {
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

	// 按医学考试标准题型顺序排列：A1 → A2 → A3/A4 → B → 案例分析 → 其他
	sortModeOrder(modeOrder)

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
			for _, grp := range greedyFillRNG(pool, need, opts.rng) {
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
			resultGroups = append(resultGroups, greedyFillRNG(pool, need, opts.rng)...)
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
				// Shuffle so we don't always fall back to the same questions
				// in original document order when there's a shortfall.
				fallback := append([]group(nil), modeMap[mk]...)
				opts.rng.shuffle(fallback)
				for _, grp := range fallback {
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
			resultGroups = append(resultGroups, greedyFillRNG(pool, need, opts.rng)...)
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
				// Shuffle the fallback pool so shortfall fill is not
				// deterministic (otherwise the same tail questions keep
				// appearing at the end of the generated paper).
				fallback := append([]group(nil), modeMap[mk]...)
				opts.rng.shuffle(fallback)
				for _, grp := range fallback {
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
	return greedyFillRNG(pool, target, nil)
}

// greedyFillRNG is like greedyFill but randomises tie-breaks when an RNG is
// supplied, so the tail of the selection is not deterministic.
func greedyFillRNG(pool []group, target int, rng *lcgRNG) []group {
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
	// Second pass: pick a group whose size is the largest that still fits.
	// Multiple groups may tie on size — collect them all and pick one at
	// random, otherwise the same group is always chosen and the tail of the
	// paper becomes deterministic.
	for n < target && len(remaining) > 0 {
		gap := target - n
		bestC := 0
		var bestIdxs []int
		for i, grp := range remaining {
			c := len(grp)
			if c > gap {
				continue
			}
			if c > bestC {
				bestC = c
				bestIdxs = bestIdxs[:0]
				bestIdxs = append(bestIdxs, i)
			} else if c == bestC {
				bestIdxs = append(bestIdxs, i)
			}
		}
		if len(bestIdxs) == 0 {
			break
		}
		chosen := bestIdxs[0]
		if rng != nil && len(bestIdxs) > 1 {
			chosen = bestIdxs[int(rng.next()%uint64(len(bestIdxs)))]
		}
		picked = append(picked, remaining[chosen])
		n += bestC
		remaining = append(remaining[:chosen], remaining[chosen+1:]...)
	}
	// Third pass: truncate a group that is larger than the remaining gap.
	// Pick the smallest oversized group (least waste), breaking ties
	// randomly. When truncating, take a random window of the group instead of
	// always taking the first N sub-questions.
	if n < target && len(remaining) > 0 {
		gap := target - n
		bestSize := 0
		var bestIdxs []int
		for i, grp := range remaining {
			c := len(grp)
			if c <= gap {
				continue
			}
			if len(bestIdxs) == 0 || c < bestSize {
				bestSize = c
				bestIdxs = bestIdxs[:0]
				bestIdxs = append(bestIdxs, i)
			} else if c == bestSize {
				bestIdxs = append(bestIdxs, i)
			}
		}
		if len(bestIdxs) > 0 {
			chosen := bestIdxs[0]
			if rng != nil && len(bestIdxs) > 1 {
				chosen = bestIdxs[int(rng.next()%uint64(len(bestIdxs)))]
			}
			src := remaining[chosen]
			startMax := len(src) - gap
			start := 0
			if rng != nil && startMax > 0 {
				start = int(rng.next() % uint64(startMax+1))
			}
			truncated := make(group, gap)
			copy(truncated, src[start:start+gap])
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

// bumpBucket 是三个限流器（通用 / img-proxy / ai-chat）共用的核心逻辑。
// 它把 map 传进来以便共享 rateMu 一把锁，避免多把锁导致的死锁风险。
// 返回 true 表示本次请求被允许（已计入 bucket），false 表示触发限流。
//
// 注意：调用方必须持有 s.rateMu。
func bumpBucket(buckets map[string][]time.Time, ip string, window time.Duration, limit int) bool {
	now := time.Now()
	cutoff := now.Add(-window)
	bucket := buckets[ip]
	fresh := bucket[:0]
	for _, t := range bucket {
		if t.After(cutoff) {
			fresh = append(fresh, t)
		}
	}
	if len(fresh) >= limit {
		buckets[ip] = fresh
		return false
	}
	buckets[ip] = append(fresh, now)
	return true
}

func (s *Server) checkRate(ip string) bool {
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	return bumpBucket(s.rateBuckets, ip, rateWindow, rateLimit)
}

// checkImgProxyRate 对 /api/img/proxy 和 /api/img/local/* 做额外限流。
// 正常做题一屏 5-10 张图的峰值在窗口内完全够用；恶意批量请求被挡住。
func (s *Server) checkImgProxyRate(ip string) bool {
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	return bumpBucket(s.imgProxyBuckets, ip, imgProxyRateWindow, imgProxyRateLimit)
}

// checkAIChatRate 对 /api/ai/chat 和 /api/ai/report 做额外限流。
// 每次 AI 请求都消耗 LLM token 成本，10 req/min 是个合理的上限。
func (s *Server) checkAIChatRate(ip string) bool {
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	return bumpBucket(s.aiChatBuckets, ip, aiChatRateWindow, aiChatRateLimit)
}

// banIP 封禁指定 IP 一段时间（duration=0 表示永久）。
func (s *Server) banIP(ip string, duration time.Duration) {
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	var until time.Time
	if duration > 0 {
		until = time.Now().Add(duration)
	}
	s.scanBans[ip] = until
}

// isBanned 检查 IP 是否在封禁名单内（过期自动移除）。
func (s *Server) isBanned(ip string) bool {
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	until, ok := s.scanBans[ip]
	if !ok {
		return false
	}
	// 零值 = 永久封禁
	if until.IsZero() {
		return true
	}
	if time.Now().Before(until) {
		return true
	}
	// 过期，自动移除
	delete(s.scanBans, ip)
	return false
}

// isScannerPath 判断请求路径是否为扫描器特征路径。
// 只要命中任意一条规则，立即封禁该 IP。
func (s *Server) isScannerPath(path, method string) bool {
	// 常见扫描路径前缀
	scanPrefixes := []string{
		"/wp-", "/wordpress", "/xmlrpc", "/wp-admin", "/wp-login",
		"/phpmyadmin", "/pma", "/myadmin",
		"/.env", "/.git", "/.svn", "/.htaccess", "/.well-known/acme-challenge",
		"/admin", "/administrator",
		"/cgi-bin", "/cgi/",
		"/shell", "/cmd", "/exec",
		"/boaform", "/boa/",
		"/solr", "/actuator", "/jolokia",
		"/telescope", "/vendor/",
		"/config", "/setup",
		"/invoke.js",                 // Ivanti/Pulse 漏洞扫描
		"/dana-na/", "/dana-cached/", // Juniper VPN 扫描
		"/remote/", "/remote/fgt",    // Fortinet 扫描
		"/Autodiscover", "/autodiscover",
		"/owa/", "/ecp/",             // Exchange 扫描
		"/.aws",
		"/etc/passwd",
		"/proc/",
	}
	p := strings.ToLower(path)
	for _, prefix := range scanPrefixes {
		if strings.HasPrefix(p, strings.ToLower(prefix)) {
			return true
		}
	}

	// 常见扫描文件后缀
	scanSuffixes := []string{
		".php", ".asp", ".aspx", ".jsp", ".cgi",
		".bak", ".sql", ".tar", ".gz", ".zip",
		".pem", ".key", ".crt", ".p12",
		".DS_Store",
		".xml",  // 单独的 .xml 不拦（manifest.xml 合法），由前缀规则过滤
	}
	for _, suffix := range scanSuffixes {
		if strings.HasSuffix(p, suffix) {
			return true
		}
	}

	// 合法路径白名单（避免误封）：到这里才检查，短路逻辑
	_ = method
	return false
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

// remoteIP returns the client IP.
//
// 安全加固（Session J）：只有当**直连的** TCP 源地址属于可信代理列表
// （s.cfg.TrustedProxies，含 loopback 时会把 127/8、::1 也视为可信）时，
// 才会信任 X-Real-IP / X-Forwarded-For header。否则一律忽略 header，
// 回落到 r.RemoteAddr。
//
// 背景：早期实现无条件信任 header，任何人都可以用 X-Real-IP 伪造源 IP：
//   - 绕过 checkRate（每次换个假 IP 就无限请求）
//   - 绕过 banIP（扫描器被封后换 header 继续）
//   - 绕过 auth.NeedsCaptcha / CheckBruteForce（匿名暴力破解访问码）
//
// 注意：该函数依赖 Server 的配置（TrustedProxies），因此改为方法而非
// 包级函数；旧的 `remoteIP(r)` 调用点全部迁移为 `s.remoteIP(r)`。
func (s *Server) remoteIP(r *http.Request) string {
	// 先拿到直连 TCP 源地址
	direct, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		direct = r.RemoteAddr
	}
	// 如果直连地址不可信，直接返回它（忽略任何 header）
	if !s.isTrustedProxy(direct) {
		return direct
	}
	// 可信代理：读 header
	if ip := r.Header.Get("X-Real-IP"); ip != "" {
		return strings.TrimSpace(strings.SplitN(ip, ",", 2)[0])
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.SplitN(xff, ",", 2)[0])
	}
	return direct
}

// isTrustedProxy 判断给定 IP 是否在可信代理列表中。
//
// 默认行为：loopback (127.0.0.0/8, ::1) 始终可信，因为服务通常就是
// 被本机上的 nginx/caddy 反代包一层。管理员可以通过 TrustedProxies
// 追加其它 CIDR / 具体 IP。
func (s *Server) isTrustedProxy(ip string) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	if parsed.IsLoopback() {
		return true
	}
	for _, entry := range s.cfg.TrustedProxies {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		// 支持 CIDR 和单 IP 两种写法
		if strings.Contains(entry, "/") {
			_, cidr, err := net.ParseCIDR(entry)
			if err == nil && cidr.Contains(parsed) {
				return true
			}
		} else {
			if ip2 := net.ParseIP(entry); ip2 != nil && ip2.Equal(parsed) {
				return true
			}
		}
	}
	return false
}

// isLoopbackRemote 判断请求的**直连**TCP 源地址是否为 loopback (127/8, ::1)。
//
// 和 s.remoteIP(r) 不同：这里只看 r.RemoteAddr，完全忽略任何 X-Real-IP /
// X-Forwarded-For header。调试端点等敏感路径必须用这个函数，防止在可信代理
// 场景下被 header 伪造绕过。
func isLoopbackRemote(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}

// decodeJSONBody 读取并解析请求体 JSON，并强制限制 body 大小，防止恶意
// 客户端用超大 JSON body 消耗服务器内存（OOM DoS）。1 MiB 对于本项目
// 所有 API（答题记录、分享配置、AI 聊天历史等）都绰绰有余；极少数真正
// 需要更大 body 的端点（目前没有）可以自行调用 http.MaxBytesReader 覆盖。
//
// 返回任何错误都意味着请求无效，调用方应立即返回 BadRequest 并中止处理。
const defaultJSONMaxBody = 1 << 20 // 1 MiB

func decodeJSONBody(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, defaultJSONMaxBody)
	return json.NewDecoder(r.Body).Decode(v)
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// s3SignedGet 发送 AWS Signature V4 签名的 GET 请求，兼容私有 bucket。
func s3SignedGet(ctx context.Context, client *http.Client, s3URL, ak, sk, region string) (*http.Response, error) {
	parsed, err := url.Parse(s3URL)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	dateStr := now.Format("20060102")
	timeStr := now.Format("20060102T150405Z")
	service := "s3"
	payloadHash := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" // SHA-256 of empty body

	host := parsed.Host
	path := parsed.EscapedPath()
	if path == "" {
		path = "/"
	}
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	canonicalHeaders := fmt.Sprintf("host:%s\nx-amz-content-sha256:%s\nx-amz-date:%s\n", host, payloadHash, timeStr)
	canonicalReq := strings.Join([]string{"GET", path, "", canonicalHeaders, signedHeaders, payloadHash}, "\n")

	scope := dateStr + "/" + region + "/" + service + "/aws4_request"
	stringToSign := "AWS4-HMAC-SHA256\n" + timeStr + "\n" + scope + "\n" +
		hex.EncodeToString(s3sha256([]byte(canonicalReq)))

	sigKey := s3hmac(s3hmac(s3hmac(s3hmac([]byte("AWS4"+sk), []byte(dateStr)), []byte(region)), []byte(service)), []byte("aws4_request"))
	signature := hex.EncodeToString(s3hmac(sigKey, []byte(stringToSign)))

	authHeader := fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s,SignedHeaders=%s,Signature=%s", ak, scope, signedHeaders, signature)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s3URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-amz-date", timeStr)
	req.Header.Set("x-amz-content-sha256", payloadHash)
	req.Header.Set("Authorization", authHeader)
	return client.Do(req)
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

func (w *responseWriterWrapper) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func logRequest(r *http.Request, status int, duration time.Duration) {
	// 日志里的 IP 只作展示用，不参与任何安全决策，所以直接用 RemoteAddr 即可
	// （安全相关路径已改为 s.remoteIP(r)，对 header 做可信代理校验）。
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	method := r.Method
	path := r.URL.Path
	if path == "" {
		path = "/"
	}
	// 静态资源简化显示
	if strings.HasPrefix(path, "/static/") {
		path = "/static/..."
	}
	logger.Debugf("%s %s %s %d %v", ip, method, path, status, duration.Round(time.Millisecond))
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
	if err := decodeJSONBody(w, r, &body); err != nil || body.Endpoint == "" {
		jsonError(w, "invalid subscription", http.StatusBadRequest)
		return
	}
	store := s.pushStores[b.Path]
	if store == nil {
		jsonError(w, "push not available", http.StatusServiceUnavailable)
		return
	}
	store.add(&body.PushSubscription, body.UID)
	logger.Infof("[push] 新订阅: uid=%s ep=%s…", body.UID, body.Endpoint[:min(40, len(body.Endpoint))])
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
	var body struct {
		Endpoint string `json:"endpoint"`
	}
	decodeJSONBody(w, r, &body)
	if store := s.pushStores[b.Path]; store != nil && body.Endpoint != "" {
		store.remove(body.Endpoint)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// POST /api/push/test — 立即发送测试推送
// 支持两种模式：
//
//	?uid=<用户ID>  → 只推给该用户
//	（无参数）     → 推给所有订阅者
//
// 每个 IP 限流：5次/小时，防止滥用。
func (s *Server) handlePushTest(w http.ResponseWriter, r *http.Request) {
	// 严格限流：每 IP 每小时最多 5 次
	ip := s.remoteIP(r)
	s.pushTestMu.Lock()
	now := time.Now()
	cutoff := now.Add(-time.Hour)
	fresh := s.pushTestBuckets[ip][:0]
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
			logger.Errorf("[push/test] 失败: %v", err)
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
			for i := range b.Questions {
				fs = append(fs, b.Questions[i].Fingerprint)
			}
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
	if !s.cfg.Debug {
		http.NotFound(w, r)
		return
	}
	// Session J 加固：调试端点**额外**要求直连地址是 loopback。
	// 必须用 r.RemoteAddr 而不是 s.remoteIP(r)：后者在可信代理场景下会返回
	// X-Forwarded-For 的值，可被伪造。调试端点只应被本机访问（SSH 隧道、
	// 反代 allow 127.0.0.1 转发等），绝不应通过可信代理链暴露。
	if !isLoopbackRemote(r) {
		http.NotFound(w, r)
		return
	}
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
			info.Sessions = ov.Sessions
			info.Attempts = ov.Attempts
			info.WrongAttempts = ov.Wrong
			info.WrongTopics = len(b.PgStore.GetWrongFingerprints(ctx, uid, b.BankID, 10000))
			banks[i] = info
			// Detailed diagnostic for first bank
			if i == 0 {
				jsonOK(w, map[string]any{
					"uid": uid, "uid_is_legacy": uid == "_legacy" || uid == "",
					"banks": banks,
					"diag":  b.PgStore.DiagAttempts(ctx, uid, b.BankID),
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

// ── Image proxy ───────────────────────────────────────────────────────────────
// imgExtFromURL 从 URL 中推断图片扩展名（用于 S3 key 生成）
func imgExtFromURL(u string) string {
	lower := strings.ToLower(u)
	for _, ext := range []string{".png", ".gif", ".webp", ".svg", ".avif", ".jpeg", ".jpg"} {
		idx := strings.Index(lower, ext)
		if idx < 0 {
			continue
		}
		after := lower[idx+len(ext):]
		if after == "" || after[0] == '?' || after[0] == '#' {
			return ext
		}
	}
	return ".jpg"
}

// GET /api/img/proxy?url=<encoded>
// 服务端代理外链图片请求，解决前端跨域（CORS）问题。
// 支持缓存头（Cache-Control: public, max-age=86400），可被 CDN / 浏览器缓存。
//
// 两个 client 对应两条独立的信任链：
//
//   s3ImgClient        — 用于调用 s.cfg.S3Endpoint（通常是本机 MinIO/RustFS
//                        如 127.0.0.1:9000 或内网私有 S3）。内网地址是合法
//                        使用，因此不能做 SSRF 过滤。
//
//   upstreamImgClient  — 用于代理用户题目里的外链图片。必须禁止任何内网
//                        地址，包括跟随重定向之后：攻击者可以构造
//                        `GET /api/img/proxy?url=https://a.com/redirect-to-metadata`
//                        让 a.com 把 302 指到 169.254.169.254。下面的
//                        CheckRedirect 对每一跳都重新跑 SSRF 检查。
var s3ImgClient = &http.Client{
	Timeout: 20 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("too many redirects")
		}
		return nil
	},
}

var upstreamImgClient = &http.Client{
	Timeout: 20 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("too many redirects")
		}
		if err := checkPublicURL(req.URL); err != nil {
			// 重定向目标落在内网/回环/metadata 地址，拒绝跟随
			return fmt.Errorf("redirect blocked: %w", err)
		}
		return nil
	},
}

// checkPublicURL 验证 URL 是否为可代理的"公网 http(s) 资源"。
// 同时被 handleImgProxy 主流程（第一次请求）与 upstreamImgClient.CheckRedirect
// （每次重定向）使用，两个位置的策略必须一致。
func checkPublicURL(u *url.URL) error {
	if u == nil {
		return fmt.Errorf("invalid url")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("only http/https allowed")
	}
	host := strings.ToLower(u.Hostname())
	// 封锁所有私网/回环地址，防止 SSRF
	// RFC1918: 10/8, 172.16/12, 192.168/16
	// 链路本地: 169.254/16, fe80::/10
	// 回环: 127/8, ::1, localhost
	// 特殊: 0.0.0.0, metadata (169.254.169.254)
	blockedPrefixes := []string{
		"localhost", "127.", "0.0.0.0",
		"10.",
		"192.168.",
		"169.254.",
		"::1", "[::1]", "fe80",
	}
	for _, blocked := range blockedPrefixes {
		if strings.HasPrefix(host, blocked) || host == "localhost" {
			return fmt.Errorf("private address blocked")
		}
	}
	// 172.16.0.0/12 覆盖 172.16.x.x - 172.31.x.x
	if strings.HasPrefix(host, "172.") {
		parts := strings.SplitN(host, ".", 3)
		if len(parts) >= 2 {
			if second, err := strconv.Atoi(parts[1]); err == nil && second >= 16 && second <= 31 {
				return fmt.Errorf("private address blocked")
			}
		}
	}
	return nil
}

func (s *Server) handleImgProxy(w http.ResponseWriter, r *http.Request) {
	rawURL := r.URL.Query().Get("url")
	if rawURL == "" {
		http.Error(w, "missing url parameter", http.StatusBadRequest)
		return
	}

	// 若已配置 S3，服务端从 S3 取图后回传给浏览器
	// 好处：MinIO/RustFS 无需对外暴露端口（9000 只对本机开放即可）
	if s.cfg.S3Endpoint != "" && s.cfg.S3Bucket != "" {
		h := sha256.Sum256([]byte(rawURL))
		keyHash := hex.EncodeToString(h[:8])
		baseKey := strings.TrimRight(s.cfg.S3Endpoint, "/") + "/" + s.cfg.S3Bucket + "/images/" + keyHash

		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()

		// img-migrate 上传时以 Content-Type 为准定后缀，URL 里未必有扩展名。
		// 这里先按 URL 推断，再补充遍历其他常见后缀，确保能命中 S3。
		// 注意：.svg 已从白名单移除（isAllowedExt），这里也同步不再探测 .svg；
		// 历史已存在的 .svg 对象将不再被 proxy 命中，会走外链降级。
		urlExt := imgExtFromURL(rawURL)
		candidates := []string{urlExt}
		for _, ext := range []string{".jpg", ".jpeg", ".png", ".gif", ".webp", ".avif"} {
			if ext != urlExt {
				candidates = append(candidates, ext)
			}
		}

		for _, ext := range candidates {
			s3URL := baseKey + ext
			var s3Resp *http.Response
			var err error
			// 有密钥时签名请求（支持私有 bucket），否则匿名请求
			if s.cfg.S3AccessKey != "" && s.cfg.S3SecretKey != "" {
				s3Resp, err = s3SignedGet(ctx, s3ImgClient, s3URL, s.cfg.S3AccessKey, s.cfg.S3SecretKey, "us-east-1")
			} else {
				var req *http.Request
				req, err = http.NewRequestWithContext(ctx, http.MethodGet, s3URL, nil)
				if err == nil {
					s3Resp, err = s3ImgClient.Do(req)
				}
			}
			if err != nil {
				break
			}
			if s3Resp.StatusCode == http.StatusOK {
				defer s3Resp.Body.Close()
				ct := s3Resp.Header.Get("Content-Type")
				if ct == "" {
					ct = "image/jpeg"
				}
				w.Header().Set("Content-Type", ct)
				w.Header().Set("Cache-Control", "public, max-age=86400")
				w.Header().Set("Access-Control-Allow-Origin", "*")
				w.Header().Set("X-Img-Source", "s3")
				if cl := s3Resp.Header.Get("Content-Length"); cl != "" {
					w.Header().Set("Content-Length", cl)
				}
				io.Copy(w, s3Resp.Body)
				return
			}
			s3Resp.Body.Close()
		}
		// S3 中未找到该图片，降级到直接代理原始 URL
	}

	// 解析 URL 后做统一的 scheme + 私网地址检查；同一套策略也会在
	// upstreamImgClient.CheckRedirect 里对每一跳重定向重新执行。
	parsed, err := url.Parse(rawURL)
	if err != nil {
		http.Error(w, "invalid url", http.StatusBadRequest)
		return
	}
	if err := checkPublicURL(parsed); err != nil {
		http.Error(w, "url rejected: "+err.Error(), http.StatusForbidden)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		http.Error(w, "invalid url", http.StatusBadRequest)
		return
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; med-exam-kit-proxy/1.0)")
	// 有些站点需要 Referer 才肯返回图片
	req.Header.Set("Referer", parsed.Scheme+"://"+parsed.Host+"/")

	resp, err := upstreamImgClient.Do(req)
	if err != nil {
		http.Error(w, "upstream fetch failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		http.Error(w, fmt.Sprintf("upstream returned %d", resp.StatusCode), http.StatusBadGateway)
		return
	}

	// 透传 Content-Type；若上游未提供则默认 image/jpeg
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "image/jpeg"
	}
	// 只允许图片类型，防止代理被滥用获取任意内容
	if !strings.HasPrefix(ct, "image/") {
		http.Error(w, "upstream content is not an image", http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=86400") // 浏览器缓存 1 天
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("X-Img-Source", "origin")
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		w.Header().Set("Content-Length", cl)
	}

	// 限制代理响应体 20 MB，防止恶意上游打满内存/带宽
	if _, err := io.Copy(w, io.LimitReader(resp.Body, 20<<20)); err != nil {
		logger.Errorf("[img-proxy] copy error for %s: %v", rawURL, err)
	}
}

// handleDebugExamSessions dumps active exam sessions metadata for troubleshooting
// the "submitted exam lost all answers / 0 分" bug. The response deliberately
// omits the answer map — only the count is shown. Enabled only under --debug.
//
// 用途：当用户反馈"交卷后得 0 分"时，运维在 /api/debug/exam-sessions 上可以
// 看到：
//   - 对应 exam_id 的 session 是否仍然存在（未被 reveal 宽限窗口过期）
//   - revealed_at：是否已经被某次 reveal 认领过，以及距今多久
//   - started_at / time_limit：时间同步是否正常（为 0 意味着 handleExamJoin
//     旧 bug 的症状，修复后应为正值）
//   - answer_count：服务端是否确实保存了答案，排除题库过滤导致全空的情况
func (s *Server) handleDebugExamSessions(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.Debug {
		http.NotFound(w, r)
		return
	}
	if !isLoopbackRemote(r) {
		http.NotFound(w, r)
		return
	}
	type sessInfo struct {
		ID          string `json:"id"`
		Ts          int64  `json:"ts"`          // unix seconds, created at
		StartedAt   int64  `json:"started_at"`  // unix ms
		TimeLimit   int    `json:"time_limit"`  // seconds
		RevealedAt  int64  `json:"revealed_at"` // unix seconds, 0 = not revealed
		AnswerCount int    `json:"answer_count"`
		AgeSec      int64  `json:"age_sec"` // now - ts
	}
	nowS := time.Now().Unix()
	s.examMu.Lock()
	list := make([]sessInfo, 0, len(s.examSessions))
	for id, v := range s.examSessions {
		list = append(list, sessInfo{
			ID:          id,
			Ts:          v.ts,
			StartedAt:   v.startedAt,
			TimeLimit:   v.timeLimit,
			RevealedAt:  v.revealedAt,
			AnswerCount: len(v.answers),
			AgeSec:      nowS - v.ts,
		})
	}
	s.examMu.Unlock()
	jsonOK(w, map[string]any{
		"now":                  nowS,
		"reveal_grace_window":  revealGraceWindow,
		"exam_session_count":   len(list),
		"exam_sessions":        list,
	})
}

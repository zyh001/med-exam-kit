package server

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/zyh001/med-exam-kit/internal/logger"

	"github.com/zyh001/med-exam-kit/internal/ai"
	"github.com/zyh001/med-exam-kit/internal/auth"
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

// aiChatLogStorer is an optional interface for AI chat compliance log persistence.
type aiChatLogStorer interface {
	SaveAIChatLog(ctx context.Context, userID string, bankID int64, fp string, sqIdx int, userAnswer string, prompt []ai.ChatMessage, history []ai.ChatMessage, response string, reasoning string, truncated bool, model string, provider string) error
	CleanupAIChatLogs(ctx context.Context, retentionDays int) (int64, error)
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
	AIProvider       string
	AIModel          string
	AIAPIKey         string
	AIBaseURL        string
	AIEnableThinking *bool
	AIMaxTokens      int // 0 = 使用默认值 2048

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

	// AI chat 日志保留天数，0 = 使用默认值 30
	AIChatLogRetentionDays int

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
	scanBans   map[string]time.Time
	httpServer *http.Server
	// 热重载
	reloadFn   ReloadFunc // 由 cmd/quiz.go 注入
	configPath string     // 当前配置文件路径（SIGHUP 时重读）
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
	// 分享码暴破防护：key="ip:token"
	shareBruteMu sync.Mutex
	shareBrute   map[string][]time.Time
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
	revealedAt int64 // unix seconds, 0 = not yet revealed
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
	SharePin       string             `json:"share_pin"`        // 6 位分享码（下发给接收方）
	Ts             int64              `json:"ts"`
	ExpiresAt      int64              `json:"expires_at"`
}

const (
	sharePinLen      = 6
	sharePinCharset  = "ABCDEFGHJKMNPQRTUVWXY346789"
	shareCookieName  = "med_exam_share"
	shareBruteWindow = 600 * time.Second
	shareBruteMax    = 5
)

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
		shareBrute:      map[string][]time.Time{},
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
	s.loadExamSessions() // 从磁盘恢复未完成的考试会话
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

	var warnedActive bool // 已经提示过有考试正在进行
	for {
		select {
		case err := <-errCh:
			return err
		case sig := <-stop:
			if active := s.activeExamCount(); active > 0 && !warnedActive {
				// 第一次收到信号且有进行中考试：持久化后警告，不立即退出
				s.persistExamSessions()
				warnedActive = true
				fmt.Fprintf(os.Stderr,
					"\n⚠  [med-exam] 收到 %v，但 %s。\n"+
						"   答案已写入磁盘，重启后可恢复。\n"+
						"   再次按 Ctrl+C 强制退出。\n\n",
					sig, s.examSessionsSummary())
				continue // 等待第二次信号
			}
			// 无活跃考试，或已警告过（第二次信号）：正常关闭
			logger.Infof("Received signal %v, shutting down...", sig)
			s.persistExamSessions() // 关闭前最后一次持久化
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := s.httpServer.Shutdown(ctx); err != nil {
				logger.Errorf("Shutdown error: %v", err)
			}
			s.Close()
			logger.Infof("Server stopped")
			return nil
		}
	}
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

	// 清理超期 AI chat 合规日志
	retention := s.cfg.AIChatLogRetentionDays
	if retention <= 0 {
		retention = 30
	}
	for i, b := range s.cfg.Banks {
		if b.PgStore != nil {
			if st, ok := b.PgStore.(aiChatLogStorer); ok {
				n, err := st.CleanupAIChatLogs(context.Background(), retention)
				if err != nil {
					logger.Errorf("[cleanup] ai_chat_logs bank=%d pg: %v", i, err)
				} else if n > 0 {
					logger.Infof("[cleanup] ai_chat_logs bank=%d pg: cleaned %d rows older than %dd", i, n, retention)
				}
			}
		}
		if b.DB != nil {
			n, err := progress.CleanupAIChatLogs(b.DB, retention)
			if err != nil {
				logger.Errorf("[cleanup] ai_chat_logs bank=%d sqlite: %v", i, err)
			} else if n > 0 {
				logger.Infof("[cleanup] ai_chat_logs bank=%d sqlite: cleaned %d rows older than %dd", i, n, retention)
			}
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
		logRequest(s, r, wrapped.statusCode, time.Since(start))
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
	// 公共资源不要求认证：SW、manifest、静态文件（CSS/JS/字体等不含敏感数据）
	pwaPublic := r.URL.Path == "/sw.js" ||
		r.URL.Path == "/manifest.json" ||
		strings.HasPrefix(r.URL.Path, "/static/") ||
		r.URL.Path == "/api/push/vapid-key"

	// 仅分享用户（有合法 share cookie、无主 auth）：
	// 允许访问有限的路径白名单，其他一律 403
	isShareUser := s.isShareUser(r)
	if isShareUser {
		if !isShareAllowedPath(r.URL.Path) {
			jsonError(wrapped, "Forbidden", http.StatusForbidden)
			return
		}
		// 放行：跳过主 auth 检查，但仍要校验 Session Token（下方会检）
	}

	if s.cfg.AccessCode != "" && !pwaPublic && !auth.IsAuthenticated(r, s.cfg.CookieSecret, s.cfg.AccessCode) && !isShareUser {
		// 带 ?share= 的 GET / 放行：让前端 JS 弹出分享码/管理员登录框
		// 哈希 #share= 不会到服务端，所以分享链接同时在 query 里带 token
		hasShareQuery := (r.URL.Path == "/" || r.URL.Path == "") && r.Method == http.MethodGet &&
			r.URL.Query().Get("share") != ""
		if hasShareQuery {
			// 放行至 index 渲染
		} else if (r.URL.Path == "/" || r.URL.Path == "") && r.Method == http.MethodGet {
			wrapped.Header().Set("Content-Type", "text/html; charset=utf-8")
			var tok, svg string
			if auth.NeedsCaptcha(ip) {
				tok, svg = auth.NewCaptcha()
			}
			io.WriteString(wrapped, auth.RenderPINPage("医考练习", "", s.cfg.PinLen, tok, svg))
			return
		} else if r.URL.Path == "/api/exam/share/auth" ||
			r.URL.Path == "/api/exam/share/status" ||
			r.URL.Path == "/api/auth" {
			// 未登录时可访问：分享码输入、状态查询、JSON 主访问码登录
		} else {
			jsonError(wrapped, "Unauthorized", http.StatusUnauthorized)
			return
		}
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
	m.HandleFunc("POST /api/exam/share/auth", s.handleExamShareAuth)
	m.HandleFunc("GET /api/exam/share/status", s.handleExamShareStatus)
	m.HandleFunc("POST /api/auth", s.handleAPIAuth)
	m.HandleFunc("GET /api/exam/join", s.handleExamJoin)
	// Favorites sync (PG mode only; SQLite falls back gracefully)
	m.HandleFunc("POST /api/favorites/sync", s.handleFavSync)
	// AI Q&A
	m.HandleFunc("POST /api/ai/chat", s.handleAIChat)
	m.HandleFunc("POST /api/ai/report", s.handleAIReport)
	m.HandleFunc("GET /api/asr/ws", s.handleASRWebSocket)
	// Image proxy (cross-origin fix)
	m.HandleFunc("GET /api/img/proxy", s.handleImgProxy)
	// Image upload to S3 + private serve (editor use)
	m.HandleFunc("POST /api/img/upload", s.handleImgUpload)
	m.HandleFunc("GET /api/img/local/", s.handleImgLocal)
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

func genSharePin() string {
	b := make([]byte, sharePinLen)
	for i := range b {
		n := make([]byte, 1)
		rand.Read(n)
		b[i] = sharePinCharset[int(n[0])%len(sharePinCharset)]
	}
	return string(b)
}

// signShareCookie 用 cookie_secret 对 token 做 HMAC 签名
func (s *Server) signShareCookie(token string) string {
	h := hmac.New(sha256.New, []byte(s.cfg.CookieSecret))
	h.Write([]byte("share:" + token))
	return hex.EncodeToString(h.Sum(nil))
}

// verifyShareCookie 返回 cookie 里包含的合法 token，否则返回空串
func (s *Server) verifyShareCookie(r *http.Request) string {
	c, err := r.Cookie(shareCookieName)
	if err != nil || c.Value == "" {
		return ""
	}
	parts := strings.SplitN(c.Value, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	token, sig := parts[0], parts[1]
	expected := s.signShareCookie(token)
	if subtle.ConstantTimeCompare([]byte(sig), []byte(expected)) != 1 {
		return ""
	}
	// 检查 token 是否仍然存在/未过期
	s.shareMu.Lock()
	cfg, ok := s.shareTokens[token]
	s.shareMu.Unlock()
	if !ok || time.Now().Unix() > cfg.ExpiresAt {
		return ""
	}
	return token
}

// isShareUser：有合法 share cookie + 无主 auth 的用户（"仅分享用户"）
func (s *Server) isShareUser(r *http.Request) bool {
	if s.cfg.AccessCode == "" {
		return false
	}
	if auth.IsAuthenticated(r, s.cfg.CookieSecret, s.cfg.AccessCode) {
		return false
	}
	return s.verifyShareCookie(r) != ""
}

// shareBruteCheck 检查分享码尝试是否超限
func (s *Server) shareBruteCheck(ip, token string) (bool, int) {
	key := ip + ":" + token
	now := time.Now()
	s.shareBruteMu.Lock()
	defer s.shareBruteMu.Unlock()
	attempts := s.shareBrute[key]
	kept := attempts[:0]
	for _, t := range attempts {
		if now.Sub(t) < shareBruteWindow {
			kept = append(kept, t)
		}
	}
	s.shareBrute[key] = kept
	if len(kept) >= shareBruteMax {
		retry := int(shareBruteWindow.Seconds()-now.Sub(kept[0]).Seconds()) + 1
		if retry < 1 {
			retry = 1
		}
		return false, retry
	}
	return true, 0
}

func (s *Server) shareBruteFail(ip, token string) {
	key := ip + ":" + token
	s.shareBruteMu.Lock()
	s.shareBrute[key] = append(s.shareBrute[key], time.Now())
	s.shareBruteMu.Unlock()
}

func (s *Server) shareBruteClear(ip, token string) {
	key := ip + ":" + token
	s.shareBruteMu.Lock()
	delete(s.shareBrute, key)
	s.shareBruteMu.Unlock()
}

// handleExamShareAuth 接收方提交 {token, pin}，验证后下发 share cookie
func isShareAllowedPath(path string) bool {
	switch path {
	case "/",
		"/sw.js", "/manifest.json",
		"/auth",
		"/static/icon.svg", "/static/icon-192.png", "/static/icon-512.png",
		"/api/info",
		"/api/exam/join", "/api/exam/reveal", "/api/exam/time",
		"/api/exam/share/auth", "/api/exam/share/status",
		"/api/ai/chat", "/api/ai/report",
		"/api/img/proxy",
		"/api/push/vapid-key":
		return true
	}
	if strings.HasPrefix(path, "/static/") || strings.HasPrefix(path, "/api/img/local/") {
		return true
	}
	return false
}

// handleExamJoin 加入分享试卷
func (s *Server) saveAIChatLogAsync(r *http.Request, bankIdx int,
	body struct {
		Fingerprint string           `json:"fingerprint"`
		SQIndex     int              `json:"sq_index"`
		UserAnswer  string           `json:"user_answer"`
		Bank        int              `json:"bank"`
		History     []ai.ChatMessage `json:"history"`
	},
	prompt []ai.ChatMessage,
	response string, reasoning string, truncated bool) {

	if response == "" && reasoning == "" {
		return // nothing to log (e.g. aborted before any content)
	}

	if bankIdx < 0 || bankIdx >= len(s.cfg.Banks) {
		bankIdx = 0
	}
	banksMu.RLock()
	if bankIdx >= len(s.cfg.Banks) {
		banksMu.RUnlock()
		return
	}
	b := s.cfg.Banks[bankIdx] // copy
	banksMu.RUnlock()
	uid := getUserID(r)
	ctx := r.Context()

	if b.PgStore != nil {
		if st, ok := b.PgStore.(aiChatLogStorer); ok {
			if err := st.SaveAIChatLog(ctx, uid, int64(b.BankID), body.Fingerprint, body.SQIndex, body.UserAnswer, prompt, body.History, response, reasoning, truncated, s.aiClient.Model, s.aiClient.Provider); err != nil {
				logger.Errorf("[ai-chat-log] PG save failed: %v", err)
			}
			return
		}
	}
	if b.DB != nil {
		if err := progress.SaveAIChatLog(b.DB, uid, body.Fingerprint, body.SQIndex, body.UserAnswer, prompt, body.History, response, reasoning, truncated, s.aiClient.Model, s.aiClient.Provider); err != nil {
			logger.Errorf("[ai-chat-log] SQLite save failed: %v", err)
		}
	}
}

// POST /api/ai/report — 根据考试结果生成流式 AI 分析报告
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
	banksMu.RLock()
	banks := append([]BankEntry(nil), s.cfg.Banks...) // snapshot
	banksMu.RUnlock()
	for _, b := range banks {
		if b.PgStore != nil {
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
	banksMu.RLock()
	banks := append([]BankEntry(nil), s.cfg.Banks...) // snapshot
	banksMu.RUnlock()
	for _, b := range banks {
		if b.PgStore != nil {
			if ps, ok := b.PgStore.(shareStorer); ok {
				ps.DeleteShareToken(ctx, token)
			}
			break
		}
	}
}

// ── Icon handlers ─────────────────────────────────────────────────────

// handleIconSVG 返回 SVG 格式图标（用于非 PWA 场景展示）。
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

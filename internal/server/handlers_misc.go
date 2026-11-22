package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/zyh001/med-exam-kit/internal/logger"
)

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
		"now":                 nowS,
		"reveal_grace_window": revealGraceWindow,
		"exam_session_count":  len(list),
		"exam_sessions":       list,
	})
}

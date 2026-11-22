package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/zyh001/med-exam-kit/internal/auth"
)

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
		//
		// 宽限期过后拒绝 reveal，但不删除 session（让 handleExamTime 仍可用）—
		// session 由 cleanExamSessions 统一清理。
		if sess.revealedAt == 0 {
			sess.revealedAt = nowS
			go s.persistExamSessions() // 记录 revealedAt，防止重启后重复交卷
		} else if nowS-sess.revealedAt > int64(revealGraceWindow) {
			// 宽限期已过，拒绝领取（但不删除 session）
			s.examMu.Unlock()
			jsonError(w, "答案领取窗口已过期", http.StatusGone)
			return
		}
		answers = sess.answers
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
	sharePin := genSharePin()

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
		SharePin:       sharePin,
		Ts:             now,
		ExpiresAt:      expiresAt,
	}

	// Try to persist to PostgreSQL if available (golang-version + PG mode).
	// Falls back to in-memory storage otherwise.
	pgPersisted := false
	banksMu.RLock()
	if bankIdx < len(s.cfg.Banks) {
		if b := s.cfg.Banks[bankIdx]; b.PgStore != nil {
			if ps, ok2 := b.PgStore.(shareStorer); ok2 {
				if data, err := json.Marshal(cfg); err == nil {
					if err2 := ps.SaveShareTokenJSON(r.Context(), token, data); err2 == nil {
						pgPersisted = true
					}
				}
			}
		}
	}
	banksMu.RUnlock()
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

	jsonOK(w, map[string]any{"ok": true, "token": token, "share_pin": sharePin})
}

// genSharePin 生成分享码（6 位无歧义字符集）

func (s *Server) handleExamShareAuth(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
		Pin   string `json:"pin"`
	}
	if err := decodeJSONBody(w, r, &body); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	body.Token = strings.TrimSpace(body.Token)
	body.Pin = strings.ToUpper(strings.TrimSpace(body.Pin))
	if body.Token == "" || body.Pin == "" {
		jsonError(w, "缺少 token 或 pin", http.StatusBadRequest)
		return
	}

	ip := s.remoteIP(r)
	allowed, retry := s.shareBruteCheck(ip, body.Token)
	if !allowed {
		jsonError(w, fmt.Sprintf("尝试次数过多，请 %d 分钟后重试", (retry+59)/60), http.StatusTooManyRequests)
		return
	}

	s.shareMu.Lock()
	cfg, ok := s.shareTokens[body.Token]
	s.shareMu.Unlock()
	if !ok || time.Now().Unix() > cfg.ExpiresAt {
		jsonError(w, "分享链接已过期或无效", http.StatusNotFound)
		return
	}
	expected := strings.ToUpper(cfg.SharePin)
	if expected == "" || subtle.ConstantTimeCompare([]byte(body.Pin), []byte(expected)) != 1 {
		s.shareBruteFail(ip, body.Token)
		jsonError(w, "分享码错误", http.StatusUnauthorized)
		return
	}
	s.shareBruteClear(ip, body.Token)

	// 下发 share cookie
	sig := s.signShareCookie(body.Token)
	secure := r.URL.Scheme == "https" || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	http.SetCookie(w, &http.Cookie{
		Name:     shareCookieName,
		Value:    body.Token + ":" + sig,
		Path:     "/",
		MaxAge:   7 * 24 * 3600,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
	})
	jsonOK(w, map[string]any{"ok": true})
}

// handleExamShareStatus 查询当前用户的 auth 状态

func (s *Server) handleExamShareStatus(w http.ResponseWriter, r *http.Request) {
	hasMain := true
	if s.cfg.AccessCode != "" {
		hasMain = auth.IsAuthenticated(r, s.cfg.CookieSecret, s.cfg.AccessCode)
	}
	shareToken := s.verifyShareCookie(r)
	jsonOK(w, map[string]any{
		"main_auth":   hasMain,
		"share_token": shareToken,
		"share_only":  shareToken != "" && !hasMain,
	})
}

// handleAPIAuth JSON 版主访问码验证

func (s *Server) handleAPIAuth(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code string `json:"code"`
	}
	if err := decodeJSONBody(w, r, &body); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	code := strings.ToUpper(strings.TrimSpace(body.Code))
	if code == "" {
		jsonError(w, "缺少 code", http.StatusBadRequest)
		return
	}
	if s.cfg.AccessCode == "" {
		jsonOK(w, map[string]any{"ok": true})
		return
	}
	ip := s.remoteIP(r)
	if auth.NeedsCaptcha(ip) {
		jsonOK(w, map[string]any{"error": "需要验证码，请在登录页输入", "need_captcha": true})
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}
	if subtle.ConstantTimeCompare([]byte(code), []byte(s.cfg.AccessCode)) == 1 {
		auth.RecordSuccess(ip)
		auth.SetAuthCookie(w, r, s.cfg.CookieSecret, s.cfg.AccessCode)
		jsonOK(w, map[string]any{"ok": true})
		return
	}
	auth.RecordFailure(ip)
	jsonError(w, "访问码不正确", http.StatusUnauthorized)
}

// isShareAllowedPath: 仅分享用户（有 share cookie 无主 auth）可访问的路径白名单

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

	banksMu.RLock()
	if cfg.BankIdx < 0 || cfg.BankIdx >= len(s.cfg.Banks) {
		banksMu.RUnlock()
		jsonError(w, "题库不存在", http.StatusNotFound)
		return
	}
	b := s.cfg.Banks[cfg.BankIdx] // copy
	banksMu.RUnlock()
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
		go s.persistExamSessions() // 新建会话后立即持久化到磁盘
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

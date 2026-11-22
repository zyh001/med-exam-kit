package server

import (
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/zyh001/med-exam-kit/internal/logger"
	"github.com/zyh001/med-exam-kit/internal/models"
	"github.com/zyh001/med-exam-kit/internal/progress"
	"github.com/zyh001/med-exam-kit/internal/store"
)

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
		Adds []struct {
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

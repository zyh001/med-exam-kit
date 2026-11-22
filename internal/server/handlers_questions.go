package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/zyh001/med-exam-kit/internal/bank"
	"github.com/zyh001/med-exam-kit/internal/models"
)

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
		go s.persistExamSessions() // 新建会话后立即持久化到磁盘
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

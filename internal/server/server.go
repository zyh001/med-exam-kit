package server

import (
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zyh001/med-exam-kit/internal/auth"
	"github.com/zyh001/med-exam-kit/internal/bank"
	"github.com/zyh001/med-exam-kit/internal/models"
	"github.com/zyh001/med-exam-kit/internal/progress"
)

type Config struct {
	Questions     []*models.Question
	DB            *sql.DB
	Assets        fs.FS
	Host          string
	Port          int
	AccessCode    string
	CookieSecret  string
	RecordEnabled bool
	BankPath      string // path to .mqb file for saving
	Password      string // encryption password
}

type Server struct {
	cfg          Config
	sessionToken string
	assetVer     string
	mux          *http.ServeMux
	rateMu       sync.Mutex
	rateBuckets  map[string][]time.Time
}

const (
	rateLimit  = 120
	rateWindow = 60 * time.Second
)

func New(cfg Config) *Server {
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
	s.registerRoutes()
	return s
}

func (s *Server) ListenAndServe() error {
	addr := fmt.Sprintf("%s:%d", s.cfg.Host, s.cfg.Port)
	return http.ListenAndServe(addr, s)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	auth.ApplySecurityHeaders(w)

	if r.URL.Path == "/auth" && r.Method == http.MethodPost {
		if s.cfg.AccessCode != "" {
			ip := remoteIP(r)
			if ok, retry := auth.CheckBruteForce(ip); !ok {
				http.Error(w, fmt.Sprintf("尝试次数过多，请 %d 秒后重试", retry), http.StatusTooManyRequests)
				return
			}
		}
		s.mux.ServeHTTP(w, r)
		return
	}
	if !s.validHost(r) {
		jsonError(w, "Forbidden", http.StatusForbidden)
		return
	}
	if s.cfg.AccessCode != "" && !auth.IsAuthenticated(r, s.cfg.CookieSecret, s.cfg.AccessCode) {
		if (r.URL.Path == "/" || r.URL.Path == "") && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			io.WriteString(w, auth.RenderPINPage("医考练习", ""))
			return
		}
		jsonError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		tok := r.Header.Get("X-Session-Token")
		if subtle.ConstantTimeCompare([]byte(tok), []byte(s.sessionToken)) != 1 {
			jsonError(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		if !s.checkRate(remoteIP(r)) {
			jsonError(w, "Too Many Requests", http.StatusTooManyRequests)
			return
		}
	}
	s.mux.ServeHTTP(w, r)
}

func (s *Server) registerRoutes() {
	m := s.mux
	m.HandleFunc("GET /{$}", s.handleIndex) // exact root only
	m.HandleFunc("GET /editor", s.handleEditor)
	m.HandleFunc("POST /auth", s.handleAuth)
	if s.cfg.Assets != nil {
		// assets/static/quiz.css → fs.Sub to "assets/static" → sub root contains quiz.css directly
		sub, err := fs.Sub(s.cfg.Assets, "assets/static")
		if err == nil {
			m.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(sub))))
		}
	}
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
	m.HandleFunc("GET /api/stats", s.handleStats)
	m.HandleFunc("GET /api/review/due", s.handleReviewDue)
	m.HandleFunc("GET /api/wrongbook", s.handleWrongbook)
	m.HandleFunc("POST /api/sync", s.handleSync)
	m.HandleFunc("GET /api/sync/status", s.handleSyncStatus)
}

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
			// Inject session_token and asset_ver into HTML
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
			// Inject session_token and asset_ver into HTML
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
	ip := remoteIP(r)
	if s.cfg.AccessCode == "" || code == s.cfg.AccessCode {
		auth.RecordSuccess(ip)
		auth.SetAuthCookie(w, r, s.cfg.CookieSecret, s.cfg.AccessCode)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	auth.RecordFailure(ip)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	io.WriteString(w, auth.RenderPINPage("医考练习", "访问码错误，请重试"))
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	modeSet := []string{}
	modeMap := map[string]bool{}
	unitSet := []string{}
	unitMap := map[string]bool{}
	unitSq := map[string]int{}
	unitModeSq := map[string]map[string]int{}
	total := 0
	for _, q := range s.cfg.Questions {
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
	bankName := "医考练习"
	if s.cfg.BankPath != "" {
		// e.g. "口腔执业医师题库.mqb" → "口腔执业医师题库"
		base := filepath.Base(s.cfg.BankPath)
		ext := filepath.Ext(base)
		bankName = strings.TrimSuffix(base, ext)
	}
	jsonOK(w, map[string]any{
		"bank_name":      bankName,
		"total_sq":       total,
		"units":          unitSet,
		"modes":          modeSet,
		"unit_sq":        unitSq,
		"unit_mode_sq":   unitModeSq,
		"session_token":  s.sessionToken,
		"asset_ver":      s.assetVer,
		"record_enabled": s.cfg.RecordEnabled,
	})
}

func (s *Server) handleQuestions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	// Editor mode: pagination with page/per_page
	if page := q.Get("page"); page != "" {
		s.handleEditorQuestions(w, q)
		return
	}

	// Quiz mode: limit-based selection
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

	rows, _ := selectQuestions(s.cfg.Questions, selectOpts{
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
	jsonOK(w, map[string]any{"total": len(rows), "items": rows})
}

// handleEditorQuestions handles the editor's paginated question list API.
func (s *Server) handleEditorQuestions(w http.ResponseWriter, q url.Values) {
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

	// Build filtered list
	var allRows []sqFlat
	for qi, question := range s.cfg.Questions {
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

			// Apply filters
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

	// Paginate
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
	if items == nil {
		items = []sqFlat{}
	}

	jsonOK(w, map[string]any{
		"page":  page,
		"pages": pages,
		"total": total,
		"items": items,
	})
}

// handleQuestion returns a single question by index for the editor.
func (s *Server) handleQuestion(w http.ResponseWriter, r *http.Request) {
	// Path is /api/question/{qi}
	path := strings.TrimPrefix(r.URL.Path, "/api/question/")
	qi, err := strconv.Atoi(path)
	if err != nil || qi < 0 || qi >= len(s.cfg.Questions) {
		jsonError(w, "question not found", http.StatusNotFound)
		return
	}

	q := s.cfg.Questions[qi]
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

// POST /api/question - create a new empty question
func (s *Server) handleCreateQuestion(w http.ResponseWriter, r *http.Request) {
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
	qi := len(s.cfg.Questions)
	s.cfg.Questions = append(s.cfg.Questions, newQ)
	jsonOK(w, map[string]any{"ok": true, "qi": qi})
}

// DELETE /api/question/{qi} - delete a question
func (s *Server) handleDeleteQuestion(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/question/")
	qi, err := strconv.Atoi(path)
	if err != nil || qi < 0 || qi >= len(s.cfg.Questions) {
		jsonError(w, "question not found", http.StatusNotFound)
		return
	}
	total := len(s.cfg.Questions) - 1
	s.cfg.Questions = append(s.cfg.Questions[:qi], s.cfg.Questions[qi+1:]...)
	jsonOK(w, map[string]any{"ok": true, "total": total})
}

// POST /api/question/{qi}/subquestion - add a sub-question
func (s *Server) handleQuestionAction(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/question/")
	parts := strings.Split(path, "/")
	qi, err := strconv.Atoi(parts[0])
	if err != nil || qi < 0 || qi >= len(s.cfg.Questions) {
		jsonError(w, "question not found", http.StatusNotFound)
		return
	}
	// Only POST /api/question/{qi}/subquestion is supported here
	if len(parts) != 2 || parts[1] != "subquestion" {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	si := len(s.cfg.Questions[qi].SubQuestions)
	s.cfg.Questions[qi].SubQuestions = append(s.cfg.Questions[qi].SubQuestions, models.SubQuestion{
		Text:    "",
		Options: []string{"A. ", "B. ", "C. ", "D. "},
		Answer:  "",
	})
	jsonOK(w, map[string]any{"ok": true, "si": si, "sub_total": len(s.cfg.Questions[qi].SubQuestions)})
}

// PUT /api/subquestion/{qi}/{si} - update a sub-question
func (s *Server) handleUpdateSubQuestion(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/subquestion/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 {
		jsonError(w, "invalid path", http.StatusBadRequest)
		return
	}
	qi, _ := strconv.Atoi(parts[0])
	si, _ := strconv.Atoi(parts[1])
	if qi < 0 || qi >= len(s.cfg.Questions) {
		jsonError(w, "question not found", http.StatusNotFound)
		return
	}
	q := s.cfg.Questions[qi]
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
	// Update question-level fields
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

// DELETE /api/subquestion/{qi}/{si} - delete a sub-question
func (s *Server) handleDeleteSubQuestion(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/subquestion/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 {
		jsonError(w, "invalid path", http.StatusBadRequest)
		return
	}
	qi, _ := strconv.Atoi(parts[0])
	si, _ := strconv.Atoi(parts[1])
	if qi < 0 || qi >= len(s.cfg.Questions) {
		jsonError(w, "question not found", http.StatusNotFound)
		return
	}
	q := s.cfg.Questions[qi]
	if si < 0 || si >= len(q.SubQuestions) {
		jsonError(w, "sub-question not found", http.StatusNotFound)
		return
	}
	q.SubQuestions = append(q.SubQuestions[:si], q.SubQuestions[si+1:]...)
	jsonOK(w, map[string]any{"ok": true, "sub_total": len(q.SubQuestions)})
}

// POST /api/replace/preview - preview find/replace
func (s *Server) handleReplacePreview(w http.ResponseWriter, r *http.Request) {
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
	for qi, q := range s.cfg.Questions {
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
				hits = append(hits, map[string]any{
					"qi": qi, "si": si,
					"preview": preview,
				})
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

// POST /api/replace - execute find/replace
func (s *Server) handleReplace(w http.ResponseWriter, r *http.Request) {
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
	for _, q := range s.cfg.Questions {
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

// POST /api/save - save the question bank back to file
func (s *Server) handleSave(w http.ResponseWriter, r *http.Request) {
	if s.cfg.BankPath == "" {
		jsonError(w, "未配置题库路径，无法保存", http.StatusBadRequest)
		return
	}
	outPath, err := bank.SaveBank(s.cfg.Questions, s.cfg.BankPath, s.cfg.Password, true, 6)
	if err != nil {
		jsonError(w, fmt.Sprintf("保存失败: %v", err), http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]any{"ok": true, "path": outPath, "count": len(s.cfg.Questions)})
}

func (s *Server) handleRecord(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.RecordEnabled {
		jsonOK(w, map[string]any{"ok": true, "skipped": true})
		return
	}
	if s.cfg.DB == nil {
		jsonError(w, "进度数据库未初始化", http.StatusServiceUnavailable)
		return
	}
	var data map[string]any
	json.NewDecoder(r.Body).Decode(&data)
	if err := progress.RecordSession(s.cfg.DB, data, getUserID(r)); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]any{"ok": true})
}

func (s *Server) handleRecordStatus(w http.ResponseWriter, r *http.Request) {
	uid := getUserID(r)
	jsonOK(w, map[string]any{
		"enabled":   s.cfg.RecordEnabled,
		"db_ready":  s.cfg.DB != nil,
		"user_id":   uid,
		"is_legacy": uid == progress.LegacyUser,
	})
}

func (s *Server) handleRecordClear(w http.ResponseWriter, r *http.Request) {
	if s.cfg.DB == nil {
		jsonOK(w, map[string]any{"ok": true, "deleted": map[string]int{}})
		return
	}
	jsonOK(w, map[string]any{"ok": true, "deleted": progress.ClearUserData(s.cfg.DB, getUserID(r))})
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	if s.cfg.DB == nil {
		jsonOK(w, map[string]any{"overall": map[string]any{}, "history": nil, "units": nil})
		return
	}
	uid := getUserID(r)
	jsonOK(w, map[string]any{
		"overall": progress.GetOverallStats(s.cfg.DB, uid),
		"history": progress.GetHistory(s.cfg.DB, uid, 30),
		"units":   progress.GetUnitStats(s.cfg.DB, uid),
	})
}

func (s *Server) handleReviewDue(w http.ResponseWriter, r *http.Request) {
	if s.cfg.DB == nil {
		jsonOK(w, map[string]any{"fingerprints": nil})
		return
	}
	jsonOK(w, map[string]any{"fingerprints": progress.GetDueFingerprints(s.cfg.DB, getUserID(r))})
}

func (s *Server) handleWrongbook(w http.ResponseWriter, r *http.Request) {
	if s.cfg.DB == nil {
		jsonOK(w, map[string]any{"items": nil})
		return
	}
	jsonOK(w, map[string]any{"items": progress.GetWrongFingerprints(s.cfg.DB, getUserID(r), 300)})
}

func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	if s.cfg.DB == nil {
		jsonOK(w, map[string]any{"ok": false, "error": "DB not initialised"})
		return
	}
	var payload struct {
		Sessions []map[string]any `json:"sessions"`
	}
	json.NewDecoder(r.Body).Decode(&payload)
	done, skipped := progress.RecordSessionsBatch(s.cfg.DB, payload.Sessions, getUserID(r))
	jsonOK(w, map[string]any{"ok": true, "processed": done, "skipped": skipped})
}

func (s *Server) handleSyncStatus(w http.ResponseWriter, r *http.Request) {
	if s.cfg.DB == nil {
		jsonOK(w, map[string]any{"session_count": 0, "last_ts": nil})
		return
	}
	jsonOK(w, progress.GetSyncStatus(s.cfg.DB, getUserID(r)))
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
		for _, mk := range modeOrder {
			need := opts.perMode[mk]
			if need <= 0 {
				continue
			}
			pool := append([]group(nil), modeMap[mk]...)
			opts.rng.shuffle(pool)
			resultGroups = append(resultGroups, greedyFill(pool, need)...)
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

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
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
	c, err := r.Cookie("med_exam_uid")
	if err != nil {
		return progress.LegacyUser
	}
	return c.Value
}

func remoteIP(r *http.Request) string {
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

package ai

import (
	"crypto/md5"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zyh001/med-exam-kit/internal/bank"
	"github.com/zyh001/med-exam-kit/internal/dedup"
	"github.com/zyh001/med-exam-kit/internal/loader"
	"github.com/zyh001/med-exam-kit/internal/models"
	"github.com/zyh001/med-exam-kit/internal/parsers"
)

const sepW = 60

// EnricherConfig holds all settings for the BankEnricher.
type EnricherConfig struct {
	BankPath       string
	OutputPath     string
	InputDir       string
	ParserMap      map[string]string
	Password       string
	Provider       string
	Model          string
	APIKey         string
	BaseURL        string
	MaxWorkers     int
	Resume         bool
	CheckpointDir  string
	ModesFilter    []string
	ChaptersFilter []string
	Limit          int
	DryRun         bool
	OnlyMissing    bool
	ApplyAI        bool
	InPlace        bool
	WriteJSON      bool
	Timeout        float64
	EnableThinking *bool // nil = auto
}

// Task represents a single sub-question enrichment job.
type Task struct {
	TaskID      string
	QI          int
	SI          int
	NeedAnswer  bool
	NeedDiscuss bool
}

// BankEnricher orchestrates batch AI enrichment of question banks.
type BankEnricher struct {
	cfg       EnricherConfig
	ckpt      *Checkpoint
	questions []*models.Question
	startTime time.Time

	// Token tracking (atomic for thread safety)
	totalPromptTokens int64
	totalCompTokens   int64
	totalRequests     int64
	totalFailures     int64
}

// NewEnricher creates a new BankEnricher.
func NewEnricher(cfg EnricherConfig) *BankEnricher {
	if cfg.MaxWorkers <= 0 {
		cfg.MaxWorkers = 4
	}
	if cfg.CheckpointDir == "" {
		cfg.CheckpointDir = "data/checkpoints"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 60
	}

	// Resolve output path
	if cfg.OutputPath == "" {
		if cfg.ApplyAI && cfg.InPlace && cfg.BankPath != "" {
			cfg.OutputPath = cfg.BankPath
		} else if cfg.BankPath != "" {
			ext := filepath.Ext(cfg.BankPath)
			cfg.OutputPath = cfg.BankPath[:len(cfg.BankPath)-len(ext)] + "_ai.mqb"
		} else if !cfg.WriteJSON {
			cfg.OutputPath = "data/output/questions_ai.mqb"
		}
	}

	return &BankEnricher{
		cfg:  cfg,
		ckpt: NewCheckpoint("ai_enrich", cfg.CheckpointDir),
	}
}

// Run executes the enrichment pipeline.
func (e *BankEnricher) Run() error {
	e.startTime = time.Now()

	// Load questions
	questions, err := e.loadQuestions()
	if err != nil {
		return err
	}
	e.questions = questions

	// Resume checkpoint
	if e.cfg.Resume {
		if n := e.ckpt.Load(); n > 0 {
			fmt.Printf("  ♻️  断点恢复：已加载 %d 条缓存记录\n", n)
		}
	}

	// Build and filter tasks
	tasks := e.buildTasks(questions)
	if e.cfg.Limit > 0 && len(tasks) > e.cfg.Limit {
		tasks = tasks[:e.cfg.Limit]
	}

	// Separate pending from already-done
	var pending []Task
	for _, t := range tasks {
		if !e.ckpt.IsDone(t.TaskID) {
			pending = append(pending, t)
		}
	}
	already := len(tasks) - len(pending)

	e.printPlan(questions, tasks, already, pending)

	if e.cfg.DryRun {
		e.dryRunPreview(pending, questions)
		return nil
	}

	// Process
	if len(pending) > 0 {
		e.process(pending, questions)
	} else {
		fmt.Println("\n  ✅ 无需处理，全部任务已完成")
	}

	// Apply results back to questions
	filled := e.applyResults(questions, tasks)

	// Write output
	e.writeOutput(questions, filled)

	elapsed := time.Since(e.startTime).Seconds()
	failCount := 0
	for _, v := range e.ckpt.Results() {
		if v == nil {
			failCount++
		}
	}
	fmt.Printf("  ⏱  总耗时: %.1fs\n", elapsed)
	if failCount == 0 {
		e.ckpt.Clear()
	} else {
		fmt.Printf("  ⚠️  %d 个小题调用失败，断点已保留，下次 --resume 可续跑\n", failCount)
	}

	// Token usage summary
	reqs := atomic.LoadInt64(&e.totalRequests)
	if reqs > 0 {
		pt := atomic.LoadInt64(&e.totalPromptTokens)
		ct := atomic.LoadInt64(&e.totalCompTokens)
		fails := atomic.LoadInt64(&e.totalFailures)
		fmt.Printf("\n  📊 Token 用量统计\n")
		fmt.Printf("  %s\n", strings.Repeat("─", 50))
		fmt.Printf("  模型:        %s\n", e.cfg.Model)
		fmt.Printf("  成功请求:    %d 次", reqs)
		if fails > 0 {
			fmt.Printf("  失败: %d 次", fails)
		}
		fmt.Println()
		fmt.Printf("  输入 tokens: %10d\n", pt)
		fmt.Printf("  输出 tokens: %10d\n", ct)
		fmt.Printf("  合计 tokens: %10d\n", pt+ct)
		fmt.Printf("  %s\n", strings.Repeat("═", 50))
	}

	return nil
}

// ── Load ──

func (e *BankEnricher) loadQuestions() ([]*models.Question, error) {
	fmt.Printf("\n%s\n", strings.Repeat("═", sepW))
	fmt.Println("  📂 加载题库")
	fmt.Println(strings.Repeat("─", sepW))

	var questions []*models.Question
	var err error

	if e.cfg.BankPath != "" {
		questions, err = bank.LoadBank(e.cfg.BankPath, e.cfg.Password)
		if err != nil {
			return nil, err
		}
		fmt.Printf("  来源文件：%s\n", e.cfg.BankPath)
	} else if e.cfg.InputDir != "" {
		pm := e.cfg.ParserMap
		if len(pm) == 0 {
			pm = parsers.DefaultParserMap
		}
		questions, err = loader.Load(e.cfg.InputDir, pm)
		if err != nil {
			return nil, err
		}
		questions = dedup.Deduplicate(questions, "strict")
		fmt.Printf("  来源目录：%s\n", e.cfg.InputDir)
	} else {
		return nil, fmt.Errorf("bank_path 和 input_dir 至少指定一个")
	}

	totalSQ := 0
	modeCount := map[string]int{}
	for _, q := range questions {
		totalSQ += len(q.SubQuestions)
		modeCount[q.Mode]++
	}
	fmt.Printf("  题目数量：%d  小题总数：%d\n", len(questions), totalSQ)
	var parts []string
	for m, c := range modeCount {
		if m == "" {
			m = "未知"
		}
		parts = append(parts, fmt.Sprintf("%s×%d", m, c))
	}
	fmt.Printf("  题型分布：%s\n", strings.Join(parts, "  "))
	fmt.Println(strings.Repeat("─", sepW))
	return questions, nil
}

// ── Tasks ──

func (e *BankEnricher) buildTasks(questions []*models.Question) []Task {
	var tasks []Task
	for qi, q := range questions {
		if len(e.cfg.ModesFilter) > 0 && !strIn(q.Mode, e.cfg.ModesFilter) {
			continue
		}
		if len(e.cfg.ChaptersFilter) > 0 && !anySubstr(q.Unit, e.cfg.ChaptersFilter) {
			continue
		}
		for si, sq := range q.SubQuestions {
			hasAnswer := strings.TrimSpace(sq.Answer) != ""
			hasDiscuss := strings.TrimSpace(sq.Discuss) != ""
			var needAnswer, needDiscuss bool
			if e.cfg.OnlyMissing {
				needAnswer = !hasAnswer
				needDiscuss = !hasDiscuss
				if !needAnswer && !needDiscuss {
					continue
				}
			} else {
				needAnswer = true
				needDiscuss = true
			}
			tasks = append(tasks, Task{
				TaskID:      taskID(q, qi, si),
				QI:          qi,
				SI:          si,
				NeedAnswer:  needAnswer,
				NeedDiscuss: needDiscuss,
			})
		}
	}
	return tasks
}

func taskID(q *models.Question, qi, si int) string {
	stem := q.Stem
	if len(stem) > 80 {
		stem = stem[:80]
	}
	base := fmt.Sprintf("%s|%d|%d|%s", q.Fingerprint, qi, si, stem)
	return fmt.Sprintf("%x", md5.Sum([]byte(base)))
}

// ── Process ──

func (e *BankEnricher) process(pending []Task, questions []*models.Question) {
	client := NewClient(e.cfg.Provider, e.cfg.APIKey, e.cfg.BaseURL, e.cfg.Model, e.cfg.Timeout)

	total := len(pending)
	var success, failed int64

	fmt.Println("\n  🚀 开始处理  按 Ctrl+C 可安全中断")

	sem := make(chan struct{}, e.cfg.MaxWorkers)
	var wg sync.WaitGroup
	var mu sync.Mutex

	for _, t := range pending {
		wg.Add(1)
		sem <- struct{}{}
		go func(task Task) {
			defer wg.Done()
			defer func() { <-sem }()

			result := e.callAI(client, task, questions)

			mu.Lock()
			done := atomic.AddInt64(&success, 0) + atomic.AddInt64(&failed, 0) + 1
			q := questions[task.QI]
			jobLabel := "答案+解析"
			if task.NeedAnswer && !task.NeedDiscuss {
				jobLabel = "补答案"
			} else if !task.NeedAnswer && task.NeedDiscuss {
				jobLabel = "补解析"
			}

			if result != nil {
				e.ckpt.Done(task.TaskID, result)
				atomic.AddInt64(&success, 1)
				preview := ""
				if a, _ := result["answer"].(string); a != "" {
					preview += "答案=" + a + "  "
				}
				if d, _ := result["discuss"].(string); d != "" {
					r := []rune(d)
					if len(r) > 25 {
						r = r[:25]
					}
					preview += string(r) + "…"
				}
				fmt.Printf("  [%d/%d] ✅  %-8s  %-10s  %s\n",
					done, total, q.Mode, jobLabel, preview)
			} else {
				e.ckpt.Done(task.TaskID, nil)
				atomic.AddInt64(&failed, 1)
				fmt.Printf("  [%d/%d] ❌  %-8s  %-10s  调用失败\n",
					done, total, q.Mode, jobLabel)
			}
			mu.Unlock()
		}(t)
	}
	wg.Wait()

	elapsed := time.Since(e.startTime).Seconds()
	s := atomic.LoadInt64(&success)
	f := atomic.LoadInt64(&failed)
	avg := 0.0
	if total > 0 {
		avg = elapsed / float64(total)
	}
	fmt.Printf("\n%s\n", strings.Repeat("─", sepW))
	fmt.Printf("  🏁 处理完成  ✅成功: %d  ❌失败: %d  共: %d  ⏱耗时: %.1fs  均速: %.1fs/题\n",
		s, f, total, elapsed, avg)
}

// ── Single AI call with retry ──

func (e *BankEnricher) callAI(client *Client, task Task, questions []*models.Question) map[string]any {
	q := questions[task.QI]
	sq := &q.SubQuestions[task.SI]

	prompt := BuildSubQuestionPrompt(q, sq, task.NeedAnswer, task.NeedDiscuss)

	pureR := IsReasoningModel(e.cfg.Model)
	hybrid := IsHybridThinkingModel(e.cfg.Model)
	useLarge := pureR || (hybrid && e.cfg.EnableThinking != nil && *e.cfg.EnableThinking)
	maxTokens := 800
	if useLarge {
		maxTokens = 8000
	}

	messages := []ChatMessage{
		{Role: "system", Content: "你是医学考试辅导专家，严格按 JSON 输出，不要 markdown。"},
		{Role: "user", Content: prompt},
	}

	const maxRetries = 3
	baseDelay := 2 * time.Second

	for attempt := 0; attempt <= maxRetries; attempt++ {
		resp, err := client.ChatCompletion(messages, 0.2, maxTokens, e.cfg.EnableThinking)
		if err == nil {
			// Track tokens
			if resp.Usage != nil {
				atomic.AddInt64(&e.totalPromptTokens, int64(resp.Usage.PromptTokens))
				atomic.AddInt64(&e.totalCompTokens, int64(resp.Usage.CompletionTokens))
				atomic.AddInt64(&e.totalRequests, 1)
			}

			content, reasoning := ExtractResponseText(resp)
			result := ParseResponse(content)
			ok, _ := ValidateResult(result, task.NeedAnswer, task.NeedDiscuss)
			if !ok && reasoning != "" {
				// Try parsing from reasoning content as fallback
				result2 := ParseResponse(reasoning)
				ok2, _ := ValidateResult(result2, task.NeedAnswer, task.NeedDiscuss)
				if ok2 {
					return result2
				}
			}
			if ok {
				return result
			}
			return nil
		}

		// Check if retryable
		errStr := strings.ToLower(err.Error())
		retryable := strings.Contains(errStr, "429") ||
			strings.Contains(errStr, "rate limit") ||
			strings.Contains(errStr, "timeout") ||
			strings.Contains(errStr, "connection") ||
			strings.Contains(errStr, "502") ||
			strings.Contains(errStr, "503")

		if retryable && attempt < maxRetries {
			delay := baseDelay * time.Duration(1<<uint(attempt))
			time.Sleep(delay)
			continue
		}

		atomic.AddInt64(&e.totalFailures, 1)
		return nil
	}

	atomic.AddInt64(&e.totalFailures, 1)
	return nil
}

// ── Apply results ──

func (e *BankEnricher) applyResults(questions []*models.Question, tasks []Task) int {
	index := map[string]Task{}
	for _, t := range tasks {
		index[t.TaskID] = t
	}

	filled := 0
	for taskID, result := range e.ckpt.Results() {
		if result == nil {
			continue
		}
		t, ok := index[taskID]
		if !ok {
			continue
		}
		sq := &questions[t.QI].SubQuestions[t.SI]
		ApplyToSubQuestion(sq, result, e.cfg.Model, e.cfg.ApplyAI)
		filled++
	}
	return filled
}

// ── Write output ──

func (e *BankEnricher) writeOutput(questions []*models.Question, filled int) {
	fmt.Printf("\n%s\n", strings.Repeat("─", sepW))
	fmt.Println("  💾 写出结果")

	if e.cfg.OutputPath != "" {
		dir := filepath.Dir(e.cfg.OutputPath)
		os.MkdirAll(dir, 0o755)
		out, err := bank.SaveBank(questions, e.cfg.OutputPath, e.cfg.Password, true, 6)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ⚠️  保存失败: %v\n", err)
			return
		}
		note := ""
		if e.cfg.OutputPath == e.cfg.BankPath {
			note = "（就地修改）"
		}
		fmt.Printf("  ✅ 回填 %d 个小题 → %s %s\n", filled, out, note)
	} else {
		fmt.Println("  ⚠️  未指定输出路径，结果未保存")
	}
}

// ── Print ──

func (e *BankEnricher) printPlan(questions []*models.Question, tasks []Task, already int, pending []Task) {
	totalSQ := 0
	noAns := 0
	noDis := 0
	for _, q := range questions {
		for _, sq := range q.SubQuestions {
			totalSQ++
			if strings.TrimSpace(sq.Answer) == "" {
				noAns++
			}
			if strings.TrimSpace(sq.Discuss) == "" {
				noDis++
			}
		}
	}

	needAns := 0
	needDis := 0
	both := 0
	for _, t := range pending {
		if t.NeedAnswer {
			needAns++
		}
		if t.NeedDiscuss {
			needDis++
		}
		if t.NeedAnswer && t.NeedDiscuss {
			both++
		}
	}

	fmt.Printf("\n  📊 题库分析\n")
	fmt.Println(strings.Repeat("─", sepW))
	fmt.Printf("  小题总数:       %6d\n", totalSQ)
	fmt.Printf("  缺答案小题:     %6d\n", noAns)
	fmt.Printf("  缺解析小题:     %6d\n", noDis)
	fmt.Printf("\n  📋 本次任务\n")
	fmt.Println(strings.Repeat("─", sepW))
	fmt.Printf("  待增强小题:     %6d\n", len(tasks))
	fmt.Printf("  已完成(缓存):   %6d\n", already)
	fmt.Printf("  待处理:         %6d\n", len(pending))
	if len(pending) > 0 {
		fmt.Printf("    ├ 补答案:     %6d\n", needAns)
		fmt.Printf("    ├ 补解析:     %6d\n", needDis)
		fmt.Printf("    └ 答案+解析:  %6d\n", both)
	}

	pureR := IsReasoningModel(e.cfg.Model)
	hybrid := IsHybridThinkingModel(e.cfg.Model)
	fmt.Printf("\n  ⚙️  AI 配置\n")
	fmt.Println(strings.Repeat("─", sepW))
	fmt.Printf("  provider:  %s\n", e.cfg.Provider)
	fmt.Printf("  model:     %s\n", e.cfg.Model)
	fmt.Printf("  workers:   %d\n", e.cfg.MaxWorkers)
	if e.cfg.ApplyAI {
		fmt.Println("  apply-ai:  是（写回 answer/discuss 正式字段）")
	} else {
		fmt.Println("  apply-ai:  否（仅写 ai_answer/ai_discuss）")
	}
	if pureR {
		fmt.Println("  推理模式:  是（纯推理模型，始终开启）")
	} else if hybrid {
		state := "关闭（加 --thinking 开启）"
		if e.cfg.EnableThinking != nil && *e.cfg.EnableThinking {
			state = "开启"
		}
		fmt.Printf("  推理模式:  混合思考模型，当前思考：%s\n", state)
	} else {
		fmt.Println("  推理模式:  否（普通模型）")
	}
	fmt.Printf("  超时设置:  %.0fs\n", e.cfg.Timeout)
	if e.cfg.OutputPath != "" {
		note := ""
		if e.cfg.OutputPath == e.cfg.BankPath {
			note = "（就地修改）"
		}
		fmt.Printf("  输出路径:  %s%s\n", e.cfg.OutputPath, note)
	}
	fmt.Println(strings.Repeat("═", sepW))
}

func (e *BankEnricher) dryRunPreview(pending []Task, questions []*models.Question) {
	fmt.Printf("\n  [DRY-RUN] 将处理 %d 个小题（不实际调用 AI）\n\n", len(pending))
	fmt.Printf("  %4s  %-8s  %-14s  %-10s  小题内容\n", "#", "题型", "章节", "任务")
	fmt.Printf("  %s  %s  %s  %s  %s\n",
		strings.Repeat("─", 4), strings.Repeat("─", 8),
		strings.Repeat("─", 14), strings.Repeat("─", 10), strings.Repeat("─", 35))

	show := pending
	if len(show) > 30 {
		show = show[:30]
	}
	for i, t := range show {
		q := questions[t.QI]
		sq := q.SubQuestions[t.SI]
		job := "答案+解析"
		if t.NeedAnswer && !t.NeedDiscuss {
			job = "补答案"
		} else if !t.NeedAnswer {
			job = "补解析"
		}
		text := sq.Text
		if text == "" {
			text = q.Stem
		}
		r := []rune(text)
		if len(r) > 35 {
			text = string(r[:35]) + "…"
		}
		unit := q.Unit
		ur := []rune(unit)
		if len(ur) > 14 {
			unit = string(ur[:14]) + "…"
		}
		fmt.Printf("  %4d  %-8s  %-14s  %-10s  %s\n", i+1, q.Mode, unit, job, text)
	}
	if len(pending) > 30 {
		fmt.Printf("\n  … 还有 %d 个（仅展示前 30）\n", len(pending)-30)
	}
}

// ── Helpers ──

func strIn(s string, list []string) bool {
	for _, v := range list {
		if s == v {
			return true
		}
	}
	return false
}

func anySubstr(s string, keywords []string) bool {
	for _, kw := range keywords {
		if strings.Contains(s, kw) {
			return true
		}
	}
	return false
}

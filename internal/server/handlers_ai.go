package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/zyh001/med-exam-kit/internal/ai"
	"github.com/zyh001/med-exam-kit/internal/models"
)

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

	// Collect full response/reasoning for compliance log
	var fullContent, fullReasoning string
	var isTruncated bool

	for {
		select {
		case <-heartbeat.C:
			fmt.Fprintf(w, ": heartbeat\n\n")
			flusher.Flush()
		case chunk, ok := <-ch:
			if !ok {
				s.saveAIChatLogAsync(r, bankIdx, body, messages, fullContent, fullReasoning, isTruncated)
				return // channel closed
			}
			if chunk.Err != nil {
				data, _ := json.Marshal(map[string]string{"error": chunk.Err.Error()})
				fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
				s.saveAIChatLogAsync(r, bankIdx, body, messages, fullContent, fullReasoning, isTruncated)
				return
			}
			fullContent += chunk.Content
			fullReasoning += chunk.ReasoningContent
			if chunk.Truncated {
				isTruncated = true
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
				s.saveAIChatLogAsync(r, bankIdx, body, messages, fullContent, fullReasoning, isTruncated)
				return
			}
			data, _ := json.Marshal(map[string]string{"content": chunk.Content, "reasoning": chunk.ReasoningContent})
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		case <-r.Context().Done():
			s.saveAIChatLogAsync(r, bankIdx, body, messages, fullContent, fullReasoning, isTruncated)
			return
		}
	}
}

// saveAIChatLogAsync persists the AI chat conversation log to the database.
// Errors are logged but never propagated — this is a non-blocking compliance audit write.

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

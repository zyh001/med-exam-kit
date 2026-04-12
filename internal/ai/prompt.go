package ai

import (
	"fmt"
	"strings"

	"github.com/zyh001/med-exam-kit/internal/models"
)

func formatOptions(options []string) string {
	labels := "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	var lines []string
	for i, opt := range options {
		key := "?"
		if i < len(labels) {
			key = string(labels[i])
		}
		lines = append(lines, fmt.Sprintf("%s. %s", key, opt))
	}
	return strings.Join(lines, "\n")
}

// BuildSubQuestionPrompt creates the prompt for a single sub-question enrichment.
func BuildSubQuestionPrompt(q *models.Question, sq *models.SubQuestion, needAnswer, needDiscuss bool) string {
	knownAnswer := strings.TrimSpace(sq.Answer)
	optionsText := formatOptions(sq.Options)

	task := "请补全答案和解析"
	if !needAnswer {
		task = "请仅补全解析（不要改答案）"
	}

	answerRule := `必须输出 "answer" 字段，值为选项字母（如 A/B/C/D 或多选 AC）`
	if !needAnswer {
		answerRule = fmt.Sprintf(`已知正确答案为 "%s"，不要改动答案，仅输出 discuss`, knownAnswer)
	}

	outputSchema := `{ "answer": "A", "discuss": "...", "confidence": 0.0 }`
	if !needAnswer {
		outputSchema = `{ "discuss": "...", "confidence": 0.0 }`
	}

	stem := q.Stem
	if stem == "" {
		stem = ""
	}
	text := sq.Text
	if text == "" {
		text = ""
	}
	mode := q.Mode
	if mode == "" {
		mode = "未知"
	}
	unit := q.Unit
	if unit == "" {
		unit = "未知"
	}
	known := knownAnswer
	if known == "" {
		known = "无"
	}

	return fmt.Sprintf(`你是医学考试辅导专家。%s，并且仅返回 JSON。

输出格式:
%s

规则:
1) %s
2) discuss 要简洁、医学上准确，要有理有据的说明为何正确并简要排除干扰项
3) confidence 为 0~1 小数
4) 禁止输出 markdown、代码块或多余文本

题目信息:
题型: %s
章节: %s
题干: %s
小题: %s
选项:
%s
已知答案: %s`, task, outputSchema, answerRule, mode, unit, stem, text, optionsText, known)
}

// BuildAIChatPrompt creates the system prompt and initial user message for AI Q&A.
func BuildAIChatPrompt(q *models.Question, sqIdx int, userAnswer string) []ChatMessage {
	sq := &q.SubQuestions[sqIdx]

	systemPrompt := `你是一位资深的医学考试辅导老师，擅长用通俗易懂的方式帮助学生理解题目背后的知识点。

你的首要任务是根据下面的题目信息进行详细分析（这算作第1轮对话，之后学生还可以追问你2次）。

分析要求：
1. 先点明本题的核心考点
2. 逐项分析每个选项，简要说明对或错的原因
3. 给出最终结论
4. 如果学生选错了，要特别指出其可能的思路误区
5. 使用医学术语要保证正确

格式要求：
- 全部使用中文回答（必要的专业名词可附英文）
- 语言简洁清晰，重点突出，避免冗长
- 使用标准 Markdown 格式输出
- 不要在回答中提及剩余提问次数`

	// Build the context message
	var b strings.Builder
	mode := q.Mode
	if mode == "" {
		mode = "未知"
	}
	b.WriteString(fmt.Sprintf("题型: %s\n", mode))

	if q.Stem != "" {
		b.WriteString(fmt.Sprintf("题干: %s\n", q.Stem))
	}
	if sq.Text != "" {
		b.WriteString(fmt.Sprintf("小题: %s\n", sq.Text))
	}

	// Options
	effOpts := sq.Options
	if len(effOpts) == 0 {
		effOpts = q.SharedOptions
	}
	if len(effOpts) > 0 {
		b.WriteString("选项:\n")
		b.WriteString(formatOptions(effOpts))
		b.WriteString("\n")
	}

	// Correct answer
	correctAnswer := sq.EffAnswer()
	if correctAnswer != "" {
		b.WriteString(fmt.Sprintf("正确答案: %s\n", correctAnswer))
	}

	// User's answer
	if userAnswer != "" {
		b.WriteString(fmt.Sprintf("我的选择: %s\n", userAnswer))
		if correctAnswer != "" && userAnswer != correctAnswer {
			b.WriteString("（我选错了）\n")
		}
	}

	// Additional context
	if sq.ErrorProne != "" {
		b.WriteString(fmt.Sprintf("易错点: %s\n", sq.ErrorProne))
	}
	if sq.Point != "" {
		b.WriteString(fmt.Sprintf("知识点: %s\n", sq.Point))
	}

	return []ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: b.String()},
	}
}

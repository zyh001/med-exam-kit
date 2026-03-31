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

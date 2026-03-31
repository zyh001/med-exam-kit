package cmd

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zyh001/med-exam-kit/internal/bank"
	"github.com/zyh001/med-exam-kit/internal/models"
	"github.com/zyh001/med-exam-kit/internal/stats"
)

var inspectCmd = &cobra.Command{
	Use:   "inspect",
	Short: "查看 .mqb 题库内容，支持过滤与搜索",
	RunE:  runInspect,
}

func init() {
	rootCmd.AddCommand(inspectCmd)
	inspectCmd.Flags().StringSlice("mode", nil, "过滤题型")
	inspectCmd.Flags().StringSlice("unit", nil, "过滤章节关键词")
	inspectCmd.Flags().String("keyword", "", "题干或题目关键词")
	inspectCmd.Flags().Bool("has-ai", false, "只显示含 AI 补全内容的题")
	inspectCmd.Flags().Bool("missing", false, "只显示缺答案或缺解析的题")
	inspectCmd.Flags().Int("limit", 20, "最多显示多少小题（0=全部）")
	inspectCmd.Flags().Bool("full", false, "显示完整解析（默认截断至 150 字）")
	inspectCmd.Flags().Bool("show-ai", false, "同时显示 AI 原始输出")
}

func runInspect(cmd *cobra.Command, args []string) error {
	bankPath, _ := cmd.Root().PersistentFlags().GetString("bank")
	password, _ := cmd.Root().PersistentFlags().GetString("password")
	modes, _ := cmd.Flags().GetStringSlice("mode")
	units, _ := cmd.Flags().GetStringSlice("unit")
	keyword, _ := cmd.Flags().GetString("keyword")
	hasAI, _ := cmd.Flags().GetBool("has-ai")
	missing, _ := cmd.Flags().GetBool("missing")
	limit, _ := cmd.Flags().GetInt("limit")
	full, _ := cmd.Flags().GetBool("full")
	showAI, _ := cmd.Flags().GetBool("show-ai")

	if bankPath == "" {
		return fmt.Errorf("请用 -b 指定题库路径")
	}

	questions, err := bank.LoadBank(bankPath, password)
	if err != nil {
		return err
	}

	W := 72

	// Print summary header
	printInspectSummary(questions, bankPath, W)

	// Collect matching (qi, si) pairs
	type pair struct{ qi, si int }
	var results []pair

	for qi, q := range questions {
		if len(modes) > 0 && !containsAny(q.Mode, modes) {
			continue
		}
		if len(units) > 0 && !containsAnySubstring(q.Unit, units) {
			continue
		}
		if keyword != "" {
			found := strings.Contains(q.Stem, keyword)
			for _, sq := range q.SubQuestions {
				if strings.Contains(sq.Text, keyword) {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		for si, sq := range q.SubQuestions {
			if hasAI && sq.AIAnswer == "" && sq.AIDiscuss == "" {
				continue
			}
			if missing && strings.TrimSpace(sq.Answer) != "" && strings.TrimSpace(sq.Discuss) != "" {
				continue
			}
			results = append(results, pair{qi, si})
		}
	}

	hasFilter := len(modes) > 0 || len(units) > 0 || keyword != "" || hasAI || missing
	if hasFilter {
		fmt.Printf("\n  🔎 过滤结果：%d 个小题\n\n", len(results))
	} else {
		show := "全部"
		if limit > 0 {
			show = fmt.Sprintf("%d", limit)
		}
		fmt.Printf("\n  📋 题目列表（前 %s 个小题）\n\n", show)
	}

	showList := results
	if limit > 0 && len(showList) > limit {
		showList = showList[:limit]
	}

	for _, p := range showList {
		q := questions[p.qi]
		sq := q.SubQuestions[p.si]
		printInspectQuestion(q, &sq, p.qi, p.si, W, full, showAI)
	}

	fmt.Println(strings.Repeat("─", W))
	if limit > 0 && len(results) > limit {
		fmt.Printf("  … 还有 %d 个，用 --limit 0 显示全部\n", len(results)-limit)
	}
	fmt.Println()
	return nil
}

func printInspectSummary(questions []*models.Question, bankPath string, W int) {
	totalQ := len(questions)
	totalSQ := 0
	noAns := 0
	noDis := 0
	hasAIC := 0
	aiAnsC := 0
	aiDisC := 0
	modeCount := map[string]int{}

	for _, q := range questions {
		modeCount[q.Mode]++
		for _, sq := range q.SubQuestions {
			totalSQ++
			if strings.TrimSpace(sq.Answer) == "" {
				noAns++
			}
			if strings.TrimSpace(sq.Discuss) == "" {
				noDis++
			}
			if sq.AIAnswer != "" || sq.AIDiscuss != "" {
				hasAIC++
			}
			if sq.AnswerSource() == "ai" {
				aiAnsC++
			}
			if sq.DiscussSource() == "ai" {
				aiDisC++
			}
		}
	}

	sep := strings.Repeat("═", W)
	line := strings.Repeat("─", W)
	fmt.Printf("\n%s\n", sep)
	fmt.Printf("  📦 题库：%s\n", bankPath)
	fmt.Println(line)
	fmt.Printf("  %-12s%6d    %-12s%6d\n", "大题数：", totalQ, "小题数：", totalSQ)
	fmt.Printf("  %-12s%6d    %-12s%6d\n", "缺答案：", noAns, "缺解析：", noDis)
	fmt.Printf("  %-12s%6d    %-12s%6d    %-14s%6d\n",
		"含AI内容：", hasAIC, "AI答案兜底：", aiAnsC, "AI解析兜底：", aiDisC)

	fmt.Printf("\n  题型分布：\n")
	modeKV := stats.Summarize(questions, false).ByMode
	for _, m := range modeKV {
		label := m.Key
		if label == "" {
			label = "未知"
		}
		fmt.Printf("    %-20s  %6d 题\n", label, m.Count)
	}
	fmt.Println(sep)
}

func printInspectQuestion(q *models.Question, sq *models.SubQuestion, qi, si, W int, full, showAI bool) {
	ansFlag := "❓"
	if strings.TrimSpace(sq.Answer) != "" {
		ansFlag = "✅"
	} else if strings.TrimSpace(sq.AIAnswer) != "" {
		ansFlag = "🤖"
	}

	disFlag := "❓"
	if strings.TrimSpace(sq.Discuss) != "" {
		disFlag = "✅"
	} else if strings.TrimSpace(sq.AIDiscuss) != "" {
		disFlag = "🤖"
	}

	aiTag := ""
	if sq.AIAnswer != "" || sq.AIDiscuss != "" {
		aiTag = " [AI]"
	}

	fmt.Println(strings.Repeat("─", W))
	fmt.Printf("  [%d-%d]  %s  %s  答案:%s  解析:%s%s\n",
		qi+1, si+1, q.Mode, q.Unit, ansFlag, disFlag, aiTag)

	if q.Stem != "" {
		stem := q.Stem
		if !full {
			stem = truncate(stem, 100)
		}
		fmt.Printf("  【题干】%s\n", stem)
	}

	text := sq.Text
	if text == "" {
		text = "(无题目文本)"
	} else if !full {
		text = truncate(text, 100)
	}
	fmt.Printf("  【题目】%s\n", text)

	// Options
	labels := "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	optPrefix := regexp.MustCompile(`^[A-Za-z][.．、]\s*`)
	for i, opt := range sq.Options {
		opt = strings.TrimSpace(opt)
		if optPrefix.MatchString(opt) {
			fmt.Printf("         %s\n", opt)
		} else {
			lbl := "?"
			if i < len(labels) {
				lbl = string(labels[i])
			}
			fmt.Printf("         %s. %s\n", lbl, opt)
		}
	}

	// Answer
	effAns := sq.EffAnswer()
	if effAns != "" {
		src := ""
		conf := ""
		if sq.AnswerSource() == "ai" {
			src = " (AI)"
			conf = fmt.Sprintf("  [置信:%.2f  模型:%s]", sq.AIConfidence, sq.AIModel)
		}
		fmt.Printf("  【答案】%s%s%s\n", effAns, src, conf)
	}

	// Discuss
	effDis := sq.EffDiscuss()
	if effDis != "" {
		src := ""
		if sq.DiscussSource() == "ai" {
			src = " (AI)"
		}
		dis := effDis
		if !full {
			dis = truncate(dis, 150)
		}
		fmt.Printf("  【解析】%s%s\n", dis, src)
	}

	// Show AI raw output
	if showAI && (sq.AIAnswer != "" || sq.AIDiscuss != "") {
		sepLen := (W - 14) / 2
		fmt.Printf("  %s AI原始输出 %s\n", strings.Repeat("─", sepLen), strings.Repeat("─", sepLen))
		if sq.AIAnswer != "" && sq.AIAnswer != sq.EffAnswer() {
			fmt.Printf("  【AI答案】%s  [置信:%.2f  模型:%s]\n",
				sq.AIAnswer, sq.AIConfidence, sq.AIModel)
		}
		if sq.AIDiscuss != "" {
			dis := sq.AIDiscuss
			if !full {
				dis = truncate(dis, 150)
			}
			note := ""
			if strings.TrimSpace(sq.AIDiscuss) == strings.TrimSpace(sq.Discuss) {
				note = "（与官方解析相同）"
			}
			fmt.Printf("  【AI解析】%s%s\n", dis, note)
		}
	}
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) > n {
		return string(runes[:n]) + "…"
	}
	return s
}

func containsAny(s string, needles []string) bool {
	for _, n := range needles {
		if s == n {
			return true
		}
	}
	return false
}

func containsAnySubstring(s string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}

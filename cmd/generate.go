package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zyh001/med-exam-kit/internal/bank"
	"github.com/zyh001/med-exam-kit/internal/dedup"
	"github.com/zyh001/med-exam-kit/internal/exam"
	"github.com/zyh001/med-exam-kit/internal/loader"
	"github.com/zyh001/med-exam-kit/internal/models"
	"github.com/zyh001/med-exam-kit/internal/parsers"
)

var generateCmd = &cobra.Command{
	Use:   "generate",
	Short: "自动组卷: 随机抽题 → 导出 Word 试卷",
	RunE:  runGenerate,
}

func init() {
	rootCmd.AddCommand(generateCmd)
	generateCmd.Flags().StringP("input", "i", "", "JSON 文件目录")
	generateCmd.Flags().StringP("output", "o", "data/output/exam", "输出路径")
	generateCmd.Flags().String("title", "模拟考试", "试卷标题")
	generateCmd.Flags().String("subtitle", "", "副标题")
	generateCmd.Flags().StringSlice("cls", nil, "限定题库分类")
	generateCmd.Flags().StringSlice("unit", nil, "限定章节")
	generateCmd.Flags().StringSlice("mode", nil, "限定题型")
	generateCmd.Flags().IntP("count", "n", 50, "总抽题数")
	generateCmd.Flags().String("count-mode", "sub", "计数模式: sub=按小题, question=按大题")
	generateCmd.Flags().String("per-mode", "", `按题型指定数量, 如 A1型题:30,A2型题:20`)
	generateCmd.Flags().String("difficulty", "", "按难度比例, 如 easy:20,medium:40,hard:30,extreme:10")
	generateCmd.Flags().String("difficulty-mode", "global", "难度分配策略: global / per_mode")
	generateCmd.Flags().Int64("seed", 0, "随机种子（0=随机）")
	generateCmd.Flags().Bool("show-answers", false, "题目中显示答案")
	generateCmd.Flags().Bool("answer-sheet", true, "末尾附答案页")
	generateCmd.Flags().Bool("show-discuss", false, "答案页附解析")
	generateCmd.Flags().Int("total-score", 100, "总分")
	generateCmd.Flags().Float64("score", 0, "每题分值（0=自动计算）")
	generateCmd.Flags().Int("time-limit", 120, "考试时间（分钟）")
	generateCmd.Flags().Bool("dedup", true, "是否去重")
}

func runGenerate(cmd *cobra.Command, args []string) error {
	bankPath, _ := cmd.Root().PersistentFlags().GetString("bank")
	password, _ := cmd.Root().PersistentFlags().GetString("password")
	inputDir, _ := cmd.Flags().GetString("input")
	output, _ := cmd.Flags().GetString("output")
	title, _ := cmd.Flags().GetString("title")
	subtitle, _ := cmd.Flags().GetString("subtitle")
	clsList, _ := cmd.Flags().GetStringSlice("cls")
	units, _ := cmd.Flags().GetStringSlice("unit")
	modes, _ := cmd.Flags().GetStringSlice("mode")
	count, _ := cmd.Flags().GetInt("count")
	countMode, _ := cmd.Flags().GetString("count-mode")
	perModeStr, _ := cmd.Flags().GetString("per-mode")
	difficultyStr, _ := cmd.Flags().GetString("difficulty")
	difficultyMode, _ := cmd.Flags().GetString("difficulty-mode")
	seed, _ := cmd.Flags().GetInt64("seed")
	showAnswers, _ := cmd.Flags().GetBool("show-answers")
	answerSheet, _ := cmd.Flags().GetBool("answer-sheet")
	showDiscuss, _ := cmd.Flags().GetBool("show-discuss")
	totalScore, _ := cmd.Flags().GetInt("total-score")
	score, _ := cmd.Flags().GetFloat64("score")
	timeLimit, _ := cmd.Flags().GetInt("time-limit")
	doDedup, _ := cmd.Flags().GetBool("dedup")

	// Load questions
	var questions []*models.Question
	var err error

	if bankPath != "" {
		questions, err = bank.LoadBank(bankPath, password)
		if err != nil {
			return err
		}
	} else if inputDir != "" {
		questions, err = loader.Load(inputDir, parsers.DefaultParserMap)
		if err != nil {
			return err
		}
		if len(questions) == 0 {
			fmt.Fprintln(os.Stderr, "题库为空。")
			os.Exit(1)
		}
		if doDedup {
			questions = dedup.Deduplicate(questions, "strict")
		}
	} else {
		return fmt.Errorf("请用 -b 指定题库路径 或 -i 指定 JSON 目录")
	}

	totalSQ := 0
	for _, q := range questions {
		totalSQ += len(q.SubQuestions)
	}
	fmt.Printf("题库加载完成: %d 道大题, %d 道小题\n", len(questions), totalSQ)

	// Parse per_mode
	perMode := map[string]int{}
	if perModeStr != "" {
		perMode, err = parseKVInt(perModeStr)
		if err != nil {
			return fmt.Errorf("无法解析 --per-mode: %w", err)
		}
	}

	// Parse difficulty
	diffDist := map[string]int{}
	if difficultyStr != "" {
		diffDist, err = parseKVInt(difficultyStr)
		if err != nil {
			return fmt.Errorf("无法解析 --difficulty: %w", err)
		}
		valid := map[string]bool{"easy": true, "medium": true, "hard": true, "extreme": true}
		for k := range diffDist {
			if !valid[k] {
				return fmt.Errorf("无效难度等级: %s，支持: easy / medium / hard / extreme", k)
			}
		}
	}

	cfg := exam.Config{
		Title:          title,
		Subtitle:       subtitle,
		TimeLimit:      timeLimit,
		TotalScore:     totalScore,
		ScorePerSub:    score,
		ClsList:        clsList,
		Units:          units,
		Modes:          modes,
		Count:          count,
		CountMode:      countMode,
		PerMode:        perMode,
		DifficultyDist: diffDist,
		DifficultyMode: difficultyMode,
		Seed:           seed,
		ShowAnswers:    showAnswers,
		AnswerSheet:    answerSheet,
		ShowDiscuss:    showDiscuss,
	}

	gen := exam.NewGenerator(questions, cfg)
	selected, err := gen.Generate()
	if err != nil {
		return err
	}

	fmt.Println(gen.Summary(selected))

	fp, err := exam.ExportDOCX(selected, cfg, output)
	if err != nil {
		return fmt.Errorf("导出失败: %w", err)
	}
	fmt.Printf("✅ 试卷已生成: %s\n", fp)
	return nil
}

// parseKVInt parses "key1:val1,key2:val2" or JSON into map[string]int.
func parseKVInt(raw string) (map[string]int, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "{") {
		var m map[string]int
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			return nil, err
		}
		return m, nil
	}

	result := map[string]int{}
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		idx := strings.LastIndex(pair, ":")
		if idx < 0 {
			return nil, fmt.Errorf("无效格式: %q", pair)
		}
		key := strings.TrimSpace(pair[:idx])
		val := strings.TrimSpace(pair[idx+1:])
		var n int
		if _, err := fmt.Sscanf(val, "%d", &n); err != nil {
			return nil, fmt.Errorf("无法解析数字 %q", val)
		}
		result[key] = n
	}
	return result, nil
}

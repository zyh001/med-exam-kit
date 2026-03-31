package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/zyh001/med-exam-kit/internal/bank"
	"github.com/zyh001/med-exam-kit/internal/dedup"
	"github.com/zyh001/med-exam-kit/internal/loader"
	"github.com/zyh001/med-exam-kit/internal/models"
	"github.com/zyh001/med-exam-kit/internal/parsers"
	"github.com/zyh001/med-exam-kit/internal/stats"
)

var buildCmd = &cobra.Command{
	Use:   "build",
	Short: "构建题库缓存 (.mqb), 已有文件时自动追加去重",
	RunE:  runBuild,
}

func init() {
	rootCmd.AddCommand(buildCmd)
	buildCmd.Flags().StringP("input", "i", "data/raw", "JSON 文件目录")
	buildCmd.Flags().StringP("output", "o", "data/output/questions", "输出路径（自动添加 .mqb）")
	buildCmd.Flags().String("strategy", "strict", "去重策略: strict 或 content")
	buildCmd.Flags().Bool("no-compress", false, "禁用 zlib 压缩")
	buildCmd.Flags().Bool("rebuild", false, "强制重建, 忽略已有题库")
}

func runBuild(cmd *cobra.Command, args []string) error {
	inputDir, _ := cmd.Flags().GetString("input")
	output, _ := cmd.Flags().GetString("output")
	strategy, _ := cmd.Flags().GetString("strategy")
	noCompress, _ := cmd.Flags().GetBool("no-compress")
	rebuild, _ := cmd.Flags().GetBool("rebuild")
	password, _ := cmd.Root().PersistentFlags().GetString("password")

	_ = parsers.DefaultParserMap

	// Determine bank path
	bankPath := output
	if filepath.Ext(bankPath) != ".mqb" {
		bankPath += ".mqb"
	}

	// Load existing bank if present and not rebuilding
	var existing []*models.Question
	if !rebuild {
		if _, err := os.Stat(bankPath); err == nil {
			fmt.Printf("📦 发现已有题库: %s\n", filepath.Base(bankPath))
			existing, err = bank.LoadBank(bankPath, password)
			if err != nil {
				return fmt.Errorf("加载已有题库失败: %w", err)
			}
			existingSQ := countSubQ(existing)
			fmt.Printf("   已有 %d 道大题, %d 道小题\n", len(existing), existingSQ)
		}
	}

	// Load new JSON files
	fmt.Println("📂 加载 JSON...")
	newQuestions, err := loader.Load(inputDir, parsers.DefaultParserMap)
	if err != nil {
		return fmt.Errorf("加载失败：%w", err)
	}
	if len(newQuestions) == 0 && len(existing) == 0 {
		fmt.Println("未找到题目。")
		return nil
	}

	newSQ := countSubQ(newQuestions)
	if len(existing) > 0 {
		fmt.Printf("📥 发现 %d 道待追加大题, %d 道小题\n", len(newQuestions), newSQ)
	}

	// Merge
	var combined []*models.Question
	if len(existing) > 0 {
		combined = append(existing, newQuestions...)
	} else {
		combined = newQuestions
	}

	// Deduplicate
	fmt.Println("🔍 去重中...")
	combined = dedup.Deduplicate(combined, strategy)

	added := len(combined) - len(existing)
	combinedSQ := countSubQ(combined)

	// Save
	out, err := bank.SaveBank(combined, output, password, !noCompress, 6)
	if err != nil {
		return fmt.Errorf("保存失败：%w", err)
	}

	// Summary
	fmt.Printf("\n%s\n", "========================================")
	if len(existing) > 0 {
		existingSQ := countSubQ(existing)
		addedSQ := combinedSQ - existingSQ
		fmt.Printf("  原有: %d 道大题, %d 道小题\n", len(existing), existingSQ)
		fmt.Printf("  新增: %d 道大题, %d 道小题\n", added, addedSQ)
		fmt.Printf("  重复跳过: %d 道大题\n", len(newQuestions)-added)
	}
	fmt.Printf("  总计: %d 道大题, %d 道小题\n", len(combined), combinedSQ)
	fmt.Printf("  文件: %s\n", out)
	fmt.Printf("%s\n", "========================================")

	stats.PrintSummary(combined, true)
	fmt.Println("✅ 题库构建完成")
	return nil
}

func countSubQ(qs []*models.Question) int {
	n := 0
	for _, q := range qs {
		n += len(q.SubQuestions)
	}
	return n
}

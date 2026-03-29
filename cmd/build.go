package cmd

import (
	"fmt"

	"github.com/med-exam-kit/med-exam-kit/internal/bank"
	"github.com/med-exam-kit/med-exam-kit/internal/dedup"
	"github.com/med-exam-kit/med-exam-kit/internal/loader"
	"github.com/med-exam-kit/med-exam-kit/internal/parsers"
	"github.com/spf13/cobra"
)

var buildCmd = &cobra.Command{
	Use:   "build",
	Short: "从 JSON 文件构建 .mqb 题库",
	RunE:  runBuild,
}

func init() {
	rootCmd.AddCommand(buildCmd)
	buildCmd.Flags().StringP("input", "i", "data/raw", "JSON 文件目录")
	buildCmd.Flags().StringP("output", "o", "data/output/bank", "输出路径（自动添加 .mqb）")
	buildCmd.Flags().String("strategy", "strict", "去重策略: strict 或 content")
	buildCmd.Flags().Bool("no-compress", false, "禁用 zlib 压缩")
}

func runBuild(cmd *cobra.Command, args []string) error {
	inputDir, _ := cmd.Flags().GetString("input")
	output, _ := cmd.Flags().GetString("output")
	strategy, _ := cmd.Flags().GetString("strategy")
	noCompress, _ := cmd.Flags().GetBool("no-compress")
	password, _ := cmd.Root().PersistentFlags().GetString("password")

	_ = parsers.DefaultParserMap // ensure parsers are registered

	fmt.Printf("📂 扫描目录：%s\n", inputDir)
	questions, err := loader.Load(inputDir, parsers.DefaultParserMap)
	if err != nil {
		return fmt.Errorf("加载失败：%w", err)
	}
	fmt.Printf("   加载 %d 道题\n", len(questions))

	questions = dedup.Deduplicate(questions, strategy)
	fmt.Printf("   去重后 %d 道题\n", len(questions))

	out, err := bank.SaveBank(questions, output, password, !noCompress, 6)
	if err != nil {
		return fmt.Errorf("保存失败：%w", err)
	}
	fmt.Printf("✅ 题库已保存：%s\n", out)
	return nil
}

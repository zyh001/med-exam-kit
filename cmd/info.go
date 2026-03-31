package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/zyh001/med-exam-kit/internal/bank"
	"github.com/zyh001/med-exam-kit/internal/dedup"
	"github.com/zyh001/med-exam-kit/internal/loader"
	"github.com/zyh001/med-exam-kit/internal/models"
	"github.com/zyh001/med-exam-kit/internal/parsers"
	"github.com/zyh001/med-exam-kit/internal/stats"
)

var infoCmd = &cobra.Command{
	Use:   "info",
	Short: "仅查看统计信息，不导出",
	RunE:  runInfo,
}

func init() {
	rootCmd.AddCommand(infoCmd)
	infoCmd.Flags().StringP("input", "i", "", "JSON 文件目录（与 --bank 二选一）")
}

func runInfo(cmd *cobra.Command, args []string) error {
	bankPath, _ := cmd.Root().PersistentFlags().GetString("bank")
	password, _ := cmd.Root().PersistentFlags().GetString("password")
	inputDir, _ := cmd.Flags().GetString("input")

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
		if len(questions) > 0 {
			questions = dedup.Deduplicate(questions, "strict")
		}
	} else {
		return fmt.Errorf("请用 -b 指定题库路径 或 -i 指定 JSON 目录")
	}

	if len(questions) > 0 {
		stats.PrintSummary(questions, true)
	} else {
		fmt.Println("题库为空。")
	}
	return nil
}

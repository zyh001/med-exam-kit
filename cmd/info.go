package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/zyh001/med-exam-kit/internal/bank"
)

var infoCmd = &cobra.Command{
	Use:   "info",
	Short: "查看题库信息",
	RunE:  runInfo,
}

func init() {
	rootCmd.AddCommand(infoCmd)
}

func runInfo(cmd *cobra.Command, args []string) error {
	bankPath, _ := cmd.Root().PersistentFlags().GetString("bank")
	password, _ := cmd.Root().PersistentFlags().GetString("password")
	if bankPath == "" {
		return fmt.Errorf("请用 -b 指定题库路径")
	}
	questions, err := bank.LoadBank(bankPath, password)
	if err != nil {
		return err
	}
	modes := map[string]int{}
	units := map[string]int{}
	pkgs := map[string]int{}
	totalSQ := 0
	for _, q := range questions {
		modes[q.Mode]++
		units[q.Unit]++
		pkgs[q.Pkg]++
		totalSQ += len(q.SubQuestions)
	}
	fmt.Printf("题库文件：%s\n", bankPath)
	fmt.Printf("题目数量：%d 道（含 %d 个小题）\n\n", len(questions), totalSQ)
	fmt.Println("── 题型分布 ──")
	for m, n := range modes {
		fmt.Printf("  %-20s %d\n", m, n)
	}
	fmt.Printf("\n共 %d 个章节，%d 个来源\n", len(units), len(pkgs))
	return nil
}

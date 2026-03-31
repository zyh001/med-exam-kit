package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/zyh001/med-exam-kit/internal/bank"
	"github.com/zyh001/med-exam-kit/internal/dedup"
)

var reindexCmd = &cobra.Command{
	Use:    "reindex",
	Short:  "重算题库内所有指纹",
	Hidden: true,
	RunE:   runReindex,
}

func init() {
	rootCmd.AddCommand(reindexCmd)
}

func runReindex(cmd *cobra.Command, args []string) error {
	bankPath, _ := cmd.Root().PersistentFlags().GetString("bank")
	password, _ := cmd.Root().PersistentFlags().GetString("password")

	if bankPath == "" {
		return fmt.Errorf("请用 -b 指定题库路径")
	}

	questions, err := bank.LoadBank(bankPath, password)
	if err != nil {
		return err
	}

	for _, q := range questions {
		q.Fingerprint = dedup.ComputeFingerprint(q, "strict")
	}

	_, err = bank.SaveBank(questions, bankPath, password, true, 6)
	if err != nil {
		return fmt.Errorf("保存失败: %w", err)
	}

	fmt.Printf("[OK] 已重算 %d 条指纹\n", len(questions))
	return nil
}

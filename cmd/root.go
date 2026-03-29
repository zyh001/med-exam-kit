package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "med-exam",
	Short: "医学考试题库工具 (Go 版)",
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringP("password", "p", "", "题库加密密码")
	rootCmd.PersistentFlags().StringP("bank", "b", "", "题库 .mqb 文件路径")
}

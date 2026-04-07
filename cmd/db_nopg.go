//go:build nopg
// +build nopg

package cmd

import "github.com/spf13/cobra"

func init() {
	rootCmd.AddCommand(&cobra.Command{
		Use:   "db",
		Short: "结构化数据库管理（当前构建不含 PostgreSQL 支持）",
		Run: func(cmd *cobra.Command, args []string) {
			cmd.Println("⚠  此版本未包含 PostgreSQL 支持，请使用标准构建（不加 -tags nopg）。")
		},
	})
}

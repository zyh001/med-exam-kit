//go:build !nopg

package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/zyh001/med-exam-kit/internal/store/postgres"
)

func init() {
	// PersistentPreRunE：在任何 db 子命令执行前，从配置文件自动加载 DSN 和密码
	dbCmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		// 按优先级查找配置文件：--config 标志 > 当前目录 med-exam-kit.yaml
		cfgPath, _ := cmd.Flags().GetString("config")
		if cfgPath == "" {
			if _, err := os.Stat("med-exam-kit.yaml"); err == nil {
				cfgPath = "med-exam-kit.yaml"
			}
		}
		if cfgPath != "" {
			if cfg, err := loadConfig(cfgPath); err == nil {
				if dbDSN == "" && cfg.DB != "" {
					dbDSN = cfg.DB
				}
				if dbPassword == "" && cfg.Password != "" {
					dbPassword = cfg.Password
				}
			}
		}
		if dbDSN == "" {
			return fmt.Errorf("未指定数据库 DSN：请在配置文件中设置 db: postgres://... 或使用 --dsn 标志")
		}
		return nil
	}

	dbCmd.AddCommand(dbImportCmd, dbStatusCmd, dbMigrateProgressCmd, dbDeleteCmd)

	dbCmd.PersistentFlags().String("config", "", "配置文件路径（默认自动查找 med-exam-kit.yaml）")
	dbCmd.PersistentFlags().StringVar(&dbDSN, "dsn", "", "PostgreSQL DSN（留空则读取配置文件 db 字段）")

	dbImportCmd.Flags().StringVar(&dbPassword, "password", "", "题库密码（.mqb 加密时需要）")
	dbImportCmd.Flags().Int64Var(&dbImportBankID, "bank-id", 0, "追加到指定题库 ID（需配合 --append）")
	dbImportCmd.Flags().BoolVar(&dbImportAppend, "append", false, "追加模式：不删除现有题目，仅新增/更新")

	dbDeleteCmd.Flags().Int64Var(&dbDeleteBankID, "bank-id", 0, "要删除的题库 ID（必填，见 db status）")
	dbDeleteCmd.Flags().BoolVar(&dbDeleteYes, "yes", false, "跳过交互确认（脚本化删除）")
	dbDeleteCmd.Flags().BoolVar(&dbDeleteKeepProgress, "keep-progress", false, "仅删除题库和题目，保留学习记录（sessions/attempts/sm2/favorites）")
	dbDeleteCmd.MarkFlagRequired("bank-id")

	dbMigrateProgressCmd.Flags().StringVar(&dbProgressFile, "progress", "", ".progress.db SQLite 文件路径")
	dbMigrateProgressCmd.MarkFlagRequired("progress")

	rootCmd.AddCommand(dbCmd)
}

var (
	dbDSN          string
	dbPassword     string
	dbProgressFile string
)

var dbCmd = &cobra.Command{
	Use:   "db",
	Short: "结构化数据库管理（PostgreSQL）",
	Long: `管理 PostgreSQL 数据库，支持导入题库和迁移学习记录。

支持的数据库：PostgreSQL 13+（用 --dsn 指定连接串）

示例：
  # 导入题库到 PostgreSQL
  med-exam-kit db import --dsn postgres://user:pass@localhost/medexam *.mqb

  # 查看数据库状态
  med-exam-kit db status --dsn postgres://user:pass@localhost/medexam

  # 将本地 SQLite 学习记录迁移到 PostgreSQL
  med-exam-kit db migrate-progress --dsn postgres://... --progress bank.progress.db`,
}

// ── db import ──────────────────────────────────────────────────────

var (
	dbImportBankID int64
	dbImportAppend bool
)

var dbImportCmd = &cobra.Command{
	Use:   "import [flags] <file.mqb> [file2.mqb ...]",
	Short: "将 .mqb 题库文件导入到 PostgreSQL",
	Long: `将一个或多个 .mqb 题库文件导入到 PostgreSQL 数据库。

默认模式（不加任何标志）：
  按文件名作为题库名，新建或整体替换同名题库。

追加模式（--bank-id + --append）：
  将题目追加到已有题库（通过 ID 指定），已有题目按 fingerprint 更新，
  新题目直接插入，不删除现有题目。适合分批导入或补充单个题库。

示例：
  # 新建/替换题库
  med-exam-kit db import --dsn <DSN> 外科学.mqb

  # 查看现有题库 ID
  med-exam-kit db status --dsn <DSN>

  # 向题库 #1 追加题目
  med-exam-kit db import --dsn <DSN> --bank-id 1 --append 补充题目.mqb`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		pg, err := postgres.New(ctx, dbDSN)
		if err != nil {
			return fmt.Errorf("连接数据库失败: %w", err)
		}
		defer pg.Close()

		// 追加模式：--bank-id 必须与 --append 同时使用
		if dbImportBankID > 0 && !dbImportAppend {
			return fmt.Errorf("指定 --bank-id 时请同时加上 --append 标志（防止误操作覆盖题库）")
		}
		if dbImportAppend && dbImportBankID <= 0 {
			return fmt.Errorf("--append 模式需要通过 --bank-id 指定目标题库 ID")
		}

		total := 0
		for _, pattern := range args {
			files, err := filepath.Glob(pattern)
			if err != nil || len(files) == 0 {
				files = []string{pattern}
			}
			for _, path := range files {
				var importErr error
				if dbImportAppend {
					importErr = appendBankFile(ctx, pg, path, dbImportBankID)
				} else {
					importErr = importBankFile(ctx, pg, path)
				}
				if importErr != nil {
					fmt.Fprintf(os.Stderr, "  ✗ %s: %v\n", path, importErr)
					continue
				}
				total++
			}
		}
		fmt.Printf("\n✅ 已成功处理 %d 个文件\n", total)
		return nil
	},
}

func init() {
	dbRepairCmd.Flags().StringVar(&dbDSN, "dsn", "", "PostgreSQL DSN")
	dbRepairCmd.MarkFlagRequired("dsn")
	dbCmd.AddCommand(dbRepairCmd)
}

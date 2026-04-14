//go:build !nopg
// +build !nopg

package cmd

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zyh001/med-exam-kit/internal/auth"
	"github.com/zyh001/med-exam-kit/internal/bank"
	"github.com/zyh001/med-exam-kit/internal/progress"
	"github.com/zyh001/med-exam-kit/internal/server"
	pgstore "github.com/zyh001/med-exam-kit/internal/store/postgres"
)

var quizCmd = &cobra.Command{
	Use:   "quiz",
	Short: "启动刷题 Web 服务器",
	Long: `启动刷题 Web 服务器。

题库来源（二选一）：
  1. 本地 .mqb 文件（默认）：-b bank.mqb
  2. PostgreSQL 数据库：--db postgres://... --bank-id 1

学习进度存储（二选一）：
  1. 本地 SQLite（默认）：每个题库旁生成 .progress.db
  2. PostgreSQL（推荐多用户）：--db postgres://...

示例：
  # 默认本地文件模式
  med-exam-kit quiz -b exam.mqb

  # 从 PostgreSQL 加载题库（先用 db import 导入）
  med-exam-kit quiz --db postgres://user:pass@host/db --bank-id 1

  # 同一 PostgreSQL 存题库和记录
  med-exam-kit quiz --db postgres://user:pass@host/db --bank-id 1 --bank-id 2`,
	RunE: runQuiz,
}

func init() {
	rootCmd.AddCommand(quizCmd)
	quizCmd.Flags().Int("port", 5174, "监听端口")
	quizCmd.Flags().String("host", "127.0.0.1", "监听地址")
	quizCmd.Flags().Bool("no-record", false, "禁用做题记录")
	quizCmd.Flags().Bool("no-pin", false, "禁用访问码验证")
	quizCmd.Flags().String("pin", "", "自定义访问码（留空则自动生成）")
	quizCmd.Flags().StringArrayP("bank", "b", nil, "题库路径（.mqb，可重复：-b a.mqb -b b.mqb）")
	// PostgreSQL flags
	quizCmd.Flags().String("db", "", "PostgreSQL DSN（postgres://user:pass@host/db）\n留空使用本地 SQLite（默认）")
	quizCmd.Flags().Int64Slice("bank-id", nil, "从 PostgreSQL 加载的题库 ID（配合 --db 使用）")
	quizCmd.Flags().String("config", "", "配置文件路径（默认自动查找 med-exam-kit.yaml）")
	quizCmd.Flags().Int("cleanup-days", 0, "不活跃用户数据保留天数（0=使用配置文件值，默认 7 天）")
	quizCmd.Flags().Int("ai-max-tokens", 0, "AI 单次回复最大 token 数（0=使用配置文件值，默认 2048）")
}

func runQuiz(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	// ── 加载配置文件（CLI 标志优先级更高，会覆盖配置文件值）───────
	cfgFile, _ := cmd.Flags().GetString("config")
	fileCfg := defaultConfig()
	if cfgFile == "" {
		// 自动查找当前目录的默认配置文件
		if _, err := os.Stat("med-exam-kit.yaml"); err == nil {
			cfgFile = "med-exam-kit.yaml"
		}
	}
	if cfgFile != "" {
		loaded, err := loadConfig(cfgFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "⚠ 读取配置文件 %s 失败: %v\n", cfgFile, err)
		} else {
			fileCfg = loaded
			fmt.Printf("📄 已加载配置文件：%s\n", cfgFile)
		}
	}

	// CLI 标志若未显式设置则使用配置文件的值
	password, _ := cmd.Root().PersistentFlags().GetString("password")
	if password == "" { password = fileCfg.Password }

	port, _ := cmd.Flags().GetInt("port")
	if !cmd.Flags().Changed("port") { port = fileCfg.Port }

	host, _ := cmd.Flags().GetString("host")
	if !cmd.Flags().Changed("host") { host = fileCfg.Host }

	noRecord, _ := cmd.Flags().GetBool("no-record")
	if !cmd.Flags().Changed("no-record") { noRecord = fileCfg.NoRecord }

	noPin, _ := cmd.Flags().GetBool("no-pin")
	if !cmd.Flags().Changed("no-pin") { noPin = fileCfg.NoPin }

	customPin, _ := cmd.Flags().GetString("pin")
	if !cmd.Flags().Changed("pin") { customPin = fileCfg.Pin }

	cleanupDays, _ := cmd.Flags().GetInt("cleanup-days")
	if !cmd.Flags().Changed("cleanup-days") { cleanupDays = fileCfg.CleanupDays }
	if cleanupDays <= 0 { cleanupDays = 7 } // 默认 7 天

	aiMaxTokens, _ := cmd.Flags().GetInt("ai-max-tokens")
	if !cmd.Flags().Changed("ai-max-tokens") { aiMaxTokens = fileCfg.AIMaxTokens }
	if aiMaxTokens <= 0 { aiMaxTokens = 2048 } // 默认 2048

	pgDSN, _ := cmd.Flags().GetString("db")
	if !cmd.Flags().Changed("db") { pgDSN = fileCfg.DB }

	bankIDs, _ := cmd.Flags().GetInt64Slice("bank-id")
	if !cmd.Flags().Changed("bank-id") && len(fileCfg.BankIDs) > 0 { bankIDs = fileCfg.BankIDs }

	bankPaths, _ := cmd.Flags().GetStringArray("bank")
	if !cmd.Flags().Changed("bank") && len(fileCfg.Banks) > 0 { bankPaths = fileCfg.Banks }

	if len(bankPaths) == 0 && len(bankIDs) == 0 {
		return fmt.Errorf("请用 -b 指定题库路径，或用 --db + --bank-id 从数据库加载")
	}

	// ── 初始化 PostgreSQL（可选）─────────────────────────────────
	var pg *pgstore.Store
	if pgDSN != "" {
		var err error
		pg, err = pgstore.New(ctx, pgDSN)
		if err != nil {
			return fmt.Errorf("数据库连接失败: %w", err)
		}
		fmt.Println("🗄  已连接 PostgreSQL，学习记录将存储到数据库")
	}

	// ── 加载题库 ────────────────────────────────────────────────
	var banks []server.BankEntry

	// 方式1：从本地 .mqb 文件加载
	for _, bp := range bankPaths {
		fmt.Printf("📂 加载题库：%s\n", bp)
		questions, err := bank.LoadBank(bp, password)
		if err != nil {
			return fmt.Errorf("加载 %s 失败: %w", bp, err)
		}
		fmt.Printf("   共 %d 道题\n", len(questions))

		var db *sql.DB
		recordEnabled := !noRecord
		if recordEnabled {
			if pg != nil {
				// 使用 PostgreSQL 存进度
				db = nil // server 通过 pg store 访问
			} else {
				dbPath := progress.DBPathForBank(bp)
				db, err = progress.InitDB(dbPath)
				if err != nil {
					fmt.Fprintf(os.Stderr, "⚠ 无法初始化进度数据库 (%s): %v\n", bp, err)
					db = nil
					recordEnabled = false
				}
			}
		}
		entry := server.BankEntry{
			Path:          bp,
			Password:      password,
			Questions:     questions,
			DB:            db,
			RecordEnabled: recordEnabled,
		}
		if pg != nil { entry.PgStore = pg } // 避免 nil *Store 赋给 interface 变成非 nil interface
		banks = append(banks, entry)
	}

	// 方式2：从 PostgreSQL 加载题库
	if pg != nil {
		for _, bid := range bankIDs {
			fmt.Printf("🗄  从数据库加载题库 #%d ...\n", bid)
			qs, err := pg.GetBank(ctx, bid)
			if err != nil || len(qs) == 0 {
				return fmt.Errorf("数据库中未找到题库 #%d（请先运行 db import）", bid)
			}
			meta, _ := pg.ListBanks(ctx)
			name := fmt.Sprintf("bank_%d", bid)
			for _, m := range meta {
				if m.ID == bid {
					name = m.Name
					break
				}
			}
			fmt.Printf("   %s: %d 道题\n", name, len(qs))
			pgEntry := server.BankEntry{
				Path:          fmt.Sprintf("pg:bank:%d", bid),
				Name:          name,
				BankID:        int(bid),
				Questions:     qs,
				RecordEnabled: !noRecord,
			}
			if pg != nil { pgEntry.PgStore = pg }
			banks = append(banks, pgEntry)
		}
	}

	// ── 访问码 ───────────────────────────────────────────────────
	var accessCode, cookieSecret string
	pinLen := 0
	if customPin != "" {
		accessCode = strings.ToUpper(strings.TrimSpace(customPin))
		fmt.Printf("\n🔑  访问码（自定义）：%s\n", accessCode)
	} else if !noPin {
		accessCode, _ = auth.GenerateAccessCode()
		pinLen = 8
		fmt.Printf("\n🔑  访问码：%s\n", accessCode)
	}
	cookieSecret = auth.DeriveSecret(accessCode)

	cfg := server.Config{
		Banks:        banks,
		Host:         host,
		Port:         port,
		AccessCode:   accessCode,
		CookieSecret: cookieSecret,
		PinLen:       pinLen,
		AIProvider:      fileCfg.AIProvider,
		AIModel:         fileCfg.AIModel,
		AIAPIKey:        fileCfg.AIAPIKey,
		AIBaseURL:       fileCfg.AIBaseURL,
		AIEnableThinking: fileCfg.AIEnableThinking,
		AIMaxTokens:     aiMaxTokens,
		ASRAPIKey:        fileCfg.ASRAPIKey,
		ASRModel:         fileCfg.ASRModel,
		ASRBaseURL:       fileCfg.ASRBaseURL,
		S3Endpoint:      fileCfg.S3Endpoint,
		S3Bucket:        fileCfg.S3Bucket,
		S3AccessKey:     fileCfg.S3AccessKey,
		S3SecretKey:     fileCfg.S3SecretKey,
		S3PublicBase:    fileCfg.S3PublicBase,
		CleanupDays:     cleanupDays,
	}
	cfg.Assets = Assets

	fmt.Printf("\n🌐  服务地址：http://%s\n", net.JoinHostPort(host, fmt.Sprint(port)))
	if pg != nil {
		fmt.Printf("🗄  数据库模式：题库和记录均存储在 PostgreSQL\n")
	}
	if fileCfg.S3Endpoint != "" {
		fmt.Printf("🪣  S3 图片存储：%s / %s\n", fileCfg.S3Endpoint, fileCfg.S3Bucket)
	}
	if host != "127.0.0.1" {
		auth.PrintPublicWarning(port)
	}
	fmt.Println("   按 Ctrl+C 停止服务")

	srv := server.New(cfg)
	return srv.ListenAndServe()
}

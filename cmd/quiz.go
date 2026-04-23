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
	"time"

	"github.com/spf13/cobra"
	"github.com/zyh001/med-exam-kit/internal/auth"
	"github.com/zyh001/med-exam-kit/internal/bank"
	"github.com/zyh001/med-exam-kit/internal/logger"
	"github.com/zyh001/med-exam-kit/internal/models"
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
	quizCmd.Flags().Bool("debug", false, "启用调试端点 /api/debug 与 /api/debug/exam-sessions（仅排障用，切勿在生产环境开启）")
	quizCmd.Flags().Bool("auto-reload", false, "启用题库自动热重载（监视 .mqb 文件和 PG banks 表，检测到变化自动 swap）")
	quizCmd.Flags().Int("auto-reload-interval", 0, "热重载轮询间隔秒数（0=默认 10；过短会增加 DB 负载）")
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

	// 初始化日志（配置加载后立即执行，后续所有 log.Printf 都受控）
	logFile := fileCfg.LogFile
	logLevel := fileCfg.LogLevel
	if logLevel == "" {
		logLevel = "info"
	}
	if err := logger.Init(logFile, logLevel); err != nil {
		fmt.Fprintf(os.Stderr, "⚠ 日志初始化失败: %v\n", err)
	} else if logFile != "" {
		fmt.Printf("📝 日志写入：%s（级别：%s）\n", logFile, logLevel)
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

	debug, _ := cmd.Flags().GetBool("debug")
	if !cmd.Flags().Changed("debug") { debug = fileCfg.Debug }

	autoReload, _ := cmd.Flags().GetBool("auto-reload")
	if !cmd.Flags().Changed("auto-reload") { autoReload = fileCfg.AutoReloadWatch }
	autoReloadInterval, _ := cmd.Flags().GetInt("auto-reload-interval")
	if !cmd.Flags().Changed("auto-reload-interval") { autoReloadInterval = fileCfg.AutoReloadInterval }
	if autoReloadInterval <= 0 { autoReloadInterval = 10 }

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

	// ── 加载题库 ────────────────────────────────────────────────────
	banks, err := loadBankEntries(ctx, bankPaths, password, pg, noRecord)
	if err != nil {
		return err
	}
	// PostgreSQL bank IDs（额外追加）
	if pg != nil {
		for _, bid := range bankIDs {
			fmt.Printf("🗄  从数据库加载题库 #%d ...\n", bid)
			bp := fmt.Sprintf("pg:bank:%d", bid)
			e, err2 := loadBankEntries(ctx, []string{bp}, password, pg, noRecord)
			if err2 != nil {
				return err2
			}
			banks = append(banks, e...)
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
		AIChatLogRetentionDays: fileCfg.AIChatLogRetentionDays,
		Debug:           debug,
		TrustedProxies:  fileCfg.TrustedProxies,
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
	if debug {
		fmt.Println("⚠  调试模式已启用：/api/debug 与 /api/debug/exam-sessions 端点可访问（请勿在公网环境使用）")
	}
	fmt.Println("   按 Ctrl+C 停止服务")

	srv := server.New(cfg)

	// 注入热重载函数（SIGHUP / POST /api/admin/reload 使用）
	srv.SetReloadFunc(func(bankOverride []string, passwordOverride string) ([]server.BankEntry, error) {
		// 若未指定 override，重读配置文件
		paths := bankOverride
		pwd := passwordOverride
		if len(paths) == 0 && cfgFile != "" {
			reloaded, err := loadConfig(cfgFile)
			if err != nil {
				return nil, fmt.Errorf("重读配置文件失败: %w", err)
			}
			paths = reloaded.Banks
			if pwd == "" {
				pwd = reloaded.Password
			}
		}
		if len(paths) == 0 {
			return nil, fmt.Errorf("配置文件中未找到 banks 列表")
		}
		noRec := noRecord
		return loadBankEntries(ctx, paths, pwd, pg, noRec)
	})
	srv.SetConfigPath(cfgFile)

	// 写 PID 文件（方便 reload 子命令找到进程）
	pidFile := fileCfg.PidFile
	if pidFile == "" {
		pidFile = "med-exam.pid"
	}
	if err := os.WriteFile(pidFile, []byte(fmt.Sprintf("%d", os.Getpid())), 0644); err != nil {
		logger.Warnf("[pid] 写入 PID 文件失败 (%s): %v", pidFile, err)
	} else {
		defer os.Remove(pidFile)
	}

	// 启动题库自动热重载监视器（可选）：stdlib 轮询，检测到变化自动调用 HotReload
	if autoReload {
		w := NewBankWatcher(srv, time.Duration(autoReloadInterval)*time.Second,
			bankPaths, pg, bankIDs, cfgFile)
		watchCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		w.Start(watchCtx)
		fmt.Printf("👁  题库热重载监视器已启动（每 %d 秒轮询一次）\n", autoReloadInterval)
	}

	return srv.ListenAndServe()
}

// loadBankEntries 加载一批题库路径（.mqb 或 pg:bank:N），供热重载和首次启动共用。
func loadBankEntries(ctx context.Context, paths []string, password string, pg *pgstore.Store, noRecord bool) ([]server.BankEntry, error) {
	var entries []server.BankEntry
	for _, bp := range paths {
		var entry server.BankEntry
		if len(bp) > 7 && bp[:3] == "pg:" {
			// pg:bank:N
			if pg == nil {
				return nil, fmt.Errorf("题库 %s 需要 PostgreSQL 连接", bp)
			}
			var bankID int64
			fmt.Sscanf(bp, "pg:bank:%d", &bankID)
			qs, err := pg.GetBank(ctx, bankID)
			if err != nil || len(qs) == 0 {
				return nil, fmt.Errorf("数据库中未找到题库 #%d", bankID)
			}
			models.SanitizeQuestions(qs)
			meta, _ := pg.ListBanks(ctx)
			name := fmt.Sprintf("bank_%d", bankID)
			for _, m := range meta {
				if m.ID == bankID { name = m.Name; break }
			}
			entry = server.BankEntry{
				Path: bp, Name: name, BankID: int(bankID),
				Questions: qs, RecordEnabled: !noRecord,
			}
			entry.PgStore = pg
		} else {
			// local .mqb
			qs, err := bank.LoadBank(bp, password)
			if err != nil {
				return nil, fmt.Errorf("加载 %s 失败: %w", bp, err)
			}
			models.SanitizeQuestions(qs)
			var db *sql.DB
			recordEnabled := !noRecord
			if recordEnabled && pg == nil {
				dbPath := progress.DBPathForBank(bp)
				db, err = progress.InitDB(dbPath)
				if err != nil {
					logger.Warnf("[reload] 无法初始化进度数据库 (%s): %v", bp, err)
					db = nil; recordEnabled = false
				}
			}
			entry = server.BankEntry{
				Path: bp, Password: password, Questions: qs,
				DB: db, RecordEnabled: recordEnabled,
			}
			if pg != nil { entry.PgStore = pg }
		}
		entries = append(entries, entry)
	}
	return entries, nil
}



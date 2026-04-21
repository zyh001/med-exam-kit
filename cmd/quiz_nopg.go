//go:build nopg
// +build nopg

package cmd

import (
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
)

func init() {
	rootCmd.AddCommand(quizNopgCmd)
	quizNopgCmd.Flags().Int("port", 5174, "监听端口")
	quizNopgCmd.Flags().String("host", "127.0.0.1", "监听地址")
	quizNopgCmd.Flags().Bool("no-record", false, "禁用做题记录")
	quizNopgCmd.Flags().Bool("no-pin", false, "禁用访问码验证")
	quizNopgCmd.Flags().String("pin", "", "自定义访问码")
	quizNopgCmd.Flags().StringArrayP("bank", "b", nil, "题库路径")
}

var quizNopgCmd = &cobra.Command{
	Use:   "quiz",
	Short: "启动刷题 Web 服务器（本地文件模式）",
	RunE:  runQuizNopg,
}

func runQuizNopg(cmd *cobra.Command, args []string) error {
	password, _ := cmd.Root().PersistentFlags().GetString("password")
	port, _ := cmd.Flags().GetInt("port")
	host, _ := cmd.Flags().GetString("host")
	noRecord, _ := cmd.Flags().GetBool("no-record")
	noPin, _ := cmd.Flags().GetBool("no-pin")
	customPin, _ := cmd.Flags().GetString("pin")
	bankPaths, _ := cmd.Flags().GetStringArray("bank")
	if len(bankPaths) == 0 {
		return fmt.Errorf("请用 -b 指定至少一个题库路径")
	}
	var banks []server.BankEntry
	for _, bp := range bankPaths {
		fmt.Printf("📂 加载题库：%s\n", bp)
		questions, err := bank.LoadBank(bp, password)
		if err != nil { return fmt.Errorf("加载 %s 失败: %w", bp, err) }
		fmt.Printf("   共 %d 道题\n", len(questions))
		var db *sql.DB
		recordEnabled := !noRecord
		if recordEnabled {
			dbPath := progress.DBPathForBank(bp)
			db, err = progress.InitDB(dbPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "⚠ 无法初始化进度数据库: %v\n", err)
				db = nil; recordEnabled = false
			}
		}
		banks = append(banks, server.BankEntry{Path: bp, Password: password,
			Questions: questions, DB: db, RecordEnabled: recordEnabled})
	}
	var accessCode, cookieSecret string
	pinLen := 0
	if customPin != "" {
		accessCode = strings.ToUpper(strings.TrimSpace(customPin))
	} else if !noPin {
		accessCode, _ = auth.GenerateAccessCode()
		pinLen = 8
		fmt.Printf("\n🔑  访问码：%s\n", accessCode)
	}
	cookieSecret = auth.DeriveSecret(accessCode)
	cfgFile := "med-exam-kit.yaml"
	if _, err := os.Stat(cfgFile); err != nil { cfgFile = "" }
	cfg := server.Config{Banks: banks, Host: host, Port: port,
		AccessCode: accessCode, CookieSecret: cookieSecret, PinLen: pinLen}
	cfg.Assets = Assets
	fmt.Printf("\n🌐  服务地址：http://%s\n", net.JoinHostPort(host, fmt.Sprint(port)))
	srv := server.New(cfg)

	// 注入热重载函数（nopg：只支持 .mqb 文件）
	srv.SetReloadFunc(func(bankOverride []string, passwordOverride string) ([]server.BankEntry, error) {
		paths := bankOverride
		pwd := passwordOverride
		if len(paths) == 0 {
			if cfgFile == "" {
				return nil, fmt.Errorf("未找到配置文件")
			}
			reloaded, err := loadConfig(cfgFile)
			if err != nil {
				return nil, fmt.Errorf("重读配置失败: %w", err)
			}
			paths = reloaded.Banks
			if pwd == "" { pwd = reloaded.Password }
		}
		var entries []server.BankEntry
		for _, bp := range paths {
			fmt.Printf("📂 重载题库：%s\n", bp)
			qs, err := bank.LoadBank(bp, pwd)
			if err != nil {
				return nil, fmt.Errorf("加载 %s 失败: %w", bp, err)
			}
			var db *sql.DB
			dbPath := progress.DBPathForBank(bp)
			db, _ = progress.InitDB(dbPath)
			entries = append(entries, server.BankEntry{
				Path: bp, Password: pwd, Questions: qs,
				DB: db, RecordEnabled: db != nil,
			})
		}
		return entries, nil
	})
	srv.SetConfigPath(cfgFile)

	// PID 文件
	pidFile := fileCfg.PidFile
	if pidFile == "" { pidFile = "med-exam.pid" }
	if err := os.WriteFile(pidFile, []byte(fmt.Sprintf("%d", os.Getpid())), 0644); err == nil {
		defer os.Remove(pidFile)
	}

	return srv.ListenAndServe()
}

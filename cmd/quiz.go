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

var quizCmd = &cobra.Command{
	Use:   "quiz",
	Short: "启动刷题 Web 服务器",
	RunE:  runQuiz,
}

func init() {
	rootCmd.AddCommand(quizCmd)
	quizCmd.Flags().Int("port", 5174, "监听端口")
	quizCmd.Flags().String("host", "127.0.0.1", "监听地址")
	quizCmd.Flags().Bool("no-record", false, "禁用做题记录")
	quizCmd.Flags().Bool("no-pin", false, "禁用访问码验证")
	quizCmd.Flags().String("pin", "", "自定义访问码（留空则自动生成）")
	quizCmd.Flags().StringArrayP("bank", "b", nil, "题库路径（可重复：-b a.mqb -b b.mqb）")
}

func runQuiz(cmd *cobra.Command, args []string) error {
	// Collect bank paths: -B flags first, then fall back to legacy -b/--bank
	bankPaths, _ := cmd.Flags().GetStringArray("bank")
	if len(bankPaths) == 0 {
		return fmt.Errorf("请用 -b 指定至少一个题库路径（多题库：-b a.mqb -b b.mqb）")
	}

	password, _ := cmd.Root().PersistentFlags().GetString("password")
	port, _ := cmd.Flags().GetInt("port")
	host, _ := cmd.Flags().GetString("host")
	noRecord, _ := cmd.Flags().GetBool("no-record")
	noPin, _ := cmd.Flags().GetBool("no-pin")
	customPin, _ := cmd.Flags().GetString("pin")

	var banks []server.BankEntry
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
			dbPath := progress.DBPathForBank(bp)
			db, err = progress.InitDB(dbPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "⚠ 无法初始化进度数据库 (%s)：%v\n", bp, err)
				db = nil
				recordEnabled = false
			}
		}

		banks = append(banks, server.BankEntry{
			Path:          bp,
			Password:      password,
			Questions:     questions,
			DB:            db,
			RecordEnabled: recordEnabled,
		})
	}

	var accessCode, cookieSecret string
	pinLen := 0
	if customPin != "" {
		accessCode = strings.ToUpper(strings.TrimSpace(customPin))
		_, cookieSecret = auth.GenerateAccessCode()
		fmt.Printf("\n🔑  访问码（自定义）：%s\n", accessCode)
	} else if !noPin {
		accessCode, cookieSecret = auth.GenerateAccessCode()
		pinLen = 8
		fmt.Printf("\n🔑  访问码：%s\n", accessCode)
	}

	cfg := server.Config{
		Banks:        banks,
		Host:         host,
		Port:         port,
		AccessCode:   accessCode,
		CookieSecret: cookieSecret,
		PinLen:       pinLen,
	}
	cfg.Assets = Assets

	fmt.Printf("\n🌐  服务地址：http://%s\n", net.JoinHostPort(host, fmt.Sprint(port)))
	if host != "127.0.0.1" {
		auth.PrintPublicWarning(port)
	}
	fmt.Println("   按 Ctrl+C 停止服务")

	srv := server.New(cfg)
	return srv.ListenAndServe()
}

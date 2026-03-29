package cmd

import (
	"database/sql"
	"fmt"
	"net"
	"os"

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
}

func runQuiz(cmd *cobra.Command, args []string) error {
	bankPath, _ := cmd.Root().PersistentFlags().GetString("bank")
	password, _ := cmd.Root().PersistentFlags().GetString("password")
	port, _ := cmd.Flags().GetInt("port")
	host, _ := cmd.Flags().GetString("host")
	noRecord, _ := cmd.Flags().GetBool("no-record")
	noPin, _ := cmd.Flags().GetBool("no-pin")

	if bankPath == "" {
		return fmt.Errorf("请用 -b 指定题库路径")
	}

	fmt.Printf("📂 加载题库：%s\n", bankPath)
	questions, err := bank.LoadBank(bankPath, password)
	if err != nil {
		return err
	}
	fmt.Printf("   共 %d 道题\n", len(questions))

	var db *sql.DB
	recordEnabled := !noRecord
	if recordEnabled {
		dbPath := progress.DBPathForBank(bankPath)
		db, err = progress.InitDB(dbPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "⚠ 无法初始化进度数据库：%v\n", err)
			db = nil
			recordEnabled = false
		}
	}

	var accessCode, cookieSecret string
	if !noPin {
		accessCode, cookieSecret = auth.GenerateAccessCode()
		fmt.Printf("\n🔑  访问码：%s\n", accessCode)
	}

	cfg := server.Config{
		Questions:     questions,
		DB:            db,
		Host:          host,
		Port:          port,
		AccessCode:    accessCode,
		CookieSecret:  cookieSecret,
		RecordEnabled: recordEnabled,
	}

	// Try to use embedded assets from main package (injected via build tag)
	// For now assets must be copied manually; see README.
	cfg.Assets = Assets

	fmt.Printf("\n🌐  服务地址：http://%s\n", net.JoinHostPort(host, fmt.Sprint(port)))
	if host != "127.0.0.1" {
		auth.PrintPublicWarning(port)
	}
	fmt.Println("   按 Ctrl+C 停止服务")

	srv := server.New(cfg)
	return srv.ListenAndServe()
}

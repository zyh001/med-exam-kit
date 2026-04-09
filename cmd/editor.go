//go:build !nopg
// +build !nopg

package cmd

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"runtime"

	"github.com/spf13/cobra"
	"github.com/zyh001/med-exam-kit/internal/bank"
	"github.com/zyh001/med-exam-kit/internal/server"
	pgstore "github.com/zyh001/med-exam-kit/internal/store/postgres"
)

var editorCmd = &cobra.Command{
	Use:   "editor",
	Short: "启动题库编辑器 Web 服务器",
	RunE:  runEditor,
}

func init() {
	rootCmd.AddCommand(editorCmd)
	editorCmd.Flags().Int("port", 5175, "监听端口")
	editorCmd.Flags().String("host", "127.0.0.1", "监听地址")
	editorCmd.Flags().String("db", "", "PostgreSQL DSN")
	editorCmd.Flags().Int64("bank-id", 0, "要编辑的题库 ID（配合 --db）")
}

func runEditor(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	bankPath, _ := cmd.Root().PersistentFlags().GetString("bank")
	password, _ := cmd.Root().PersistentFlags().GetString("password")
	host, _ := cmd.Flags().GetString("host")
	port, _ := cmd.Flags().GetInt("port")
	pgDSN, _ := cmd.Flags().GetString("db")
	bankID, _ := cmd.Flags().GetInt64("bank-id")

	var entry server.BankEntry

	if pgDSN != "" {
		if bankID <= 0 {
			return fmt.Errorf("PG 编辑模式需要 --bank-id")
		}
		pg, err := pgstore.New(ctx, pgDSN)
		if err != nil {
			return fmt.Errorf("数据库连接失败: %w", err)
		}
		qs, err := pg.GetBank(ctx, bankID)
		if err != nil || len(qs) == 0 {
			return fmt.Errorf("数据库中未找到题库 #%d", bankID)
		}
		meta, _ := pg.ListBanks(ctx)
		name := fmt.Sprintf("bank_%d", bankID)
		for _, m := range meta {
			if m.ID == bankID { name = m.Name; break }
		}
		fmt.Printf("🗄  从数据库加载题库 #%d (%s)：%d 道题\n", bankID, name, len(qs))
		entry = server.BankEntry{
			Path: fmt.Sprintf("pg:bank:%d", bankID), Name: name,
			BankID: int(bankID), Questions: qs, PgStore: pg,
		}
	} else {
		if bankPath == "" {
			return fmt.Errorf("请用 -b 指定题库路径，或用 --db + --bank-id 编辑数据库题库")
		}
		fmt.Printf("📂 加载题库：%s\n", bankPath)
		questions, err := bank.LoadBank(bankPath, password)
		if err != nil { return err }
		fmt.Printf("   共 %d 道题\n", len(questions))
		entry = server.BankEntry{Path: bankPath, Password: password, Questions: questions}
	}

	cfg := server.Config{Banks: []server.BankEntry{entry}, Host: host, Port: port}
	cfg.Assets = Assets

	addr := net.JoinHostPort(host, fmt.Sprint(port))
	fmt.Printf("🌐 编辑器地址：http://%s/editor\n", addr)
	if pgDSN != "" { fmt.Println("🗄  保存时将写回 PostgreSQL") }
	fmt.Println("   按 Ctrl+C 停止服务")
	openBrowser(fmt.Sprintf("http://%s/editor", addr))

	return server.New(cfg).ListenAndServe()
}

func openBrowser(url string) {
	switch runtime.GOOS {
	case "darwin": exec.Command("open", url).Run()
	case "linux":  exec.Command("xdg-open", url).Run()
	case "windows": exec.Command("cmd", "/c", "start", url).Run()
	}
}

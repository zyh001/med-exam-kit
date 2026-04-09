//go:build nopg
// +build nopg

package cmd

import (
	"fmt"
	"net"
	"os/exec"
	"runtime"

	"github.com/spf13/cobra"
	"github.com/zyh001/med-exam-kit/internal/bank"
	"github.com/zyh001/med-exam-kit/internal/server"
)

var editorCmd = &cobra.Command{
	Use:   "editor",
	Short: "启动题库编辑器 Web 服务器（本地文件模式）",
	RunE:  runEditor,
}

func init() {
	rootCmd.AddCommand(editorCmd)
	editorCmd.Flags().Int("port", 5175, "监听端口")
	editorCmd.Flags().String("host", "127.0.0.1", "监听地址")
}

func runEditor(cmd *cobra.Command, args []string) error {
	bankPath, _ := cmd.Root().PersistentFlags().GetString("bank")
	password, _ := cmd.Root().PersistentFlags().GetString("password")
	host, _ := cmd.Flags().GetString("host")
	port, _ := cmd.Flags().GetInt("port")

	if bankPath == "" { return fmt.Errorf("请用 -b 指定题库路径") }
	fmt.Printf("📂 加载题库：%s\n", bankPath)
	questions, err := bank.LoadBank(bankPath, password)
	if err != nil { return err }
	fmt.Printf("   共 %d 道题\n", len(questions))

	cfg := server.Config{Banks: []server.BankEntry{{Path: bankPath, Password: password, Questions: questions}}, Host: host, Port: port}
	cfg.Assets = Assets

	addr := net.JoinHostPort(host, fmt.Sprint(port))
	fmt.Printf("🌐 编辑器地址：http://%s/editor\n", addr)
	fmt.Println("   按 Ctrl+C 停止服务")
	switch runtime.GOOS {
	case "darwin": exec.Command("open", "http://"+addr+"/editor").Run()
	case "linux":  exec.Command("xdg-open", "http://"+addr+"/editor").Run()
	case "windows": exec.Command("cmd", "/c", "start", "http://"+addr+"/editor").Run()
	}
	return server.New(cfg).ListenAndServe()
}

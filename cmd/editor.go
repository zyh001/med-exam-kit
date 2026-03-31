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
	Short: "启动题库编辑器 Web 服务器",
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

	if bankPath == "" {
		return fmt.Errorf("请用 -b 指定题库路径")
	}

	fmt.Printf("📂 加载题库：%s\n", bankPath)
	questions, err := bank.LoadBank(bankPath, password)
	if err != nil {
		return err
	}
	fmt.Printf("   共 %d 道题\n", len(questions))

	cfg := server.Config{
		Questions:     questions,
		Host:          host,
		Port:          port,
		RecordEnabled: false,
		BankPath:      bankPath,
		Password:      password,
	}
	cfg.Assets = Assets

	addr := net.JoinHostPort(host, fmt.Sprint(port))
	url := fmt.Sprintf("http://%s/editor", addr)

	fmt.Printf("🌐 编辑器地址：%s\n", url)
	fmt.Println("   按 Ctrl+C 停止服务")

	openBrowser(url)

	srv := server.New(cfg)
	return srv.ListenAndServe()
}

func openBrowser(url string) {
	var err error
	switch runtime.GOOS {
	case "darwin":
		err = exec.Command("open", url).Run()
	case "linux":
		err = exec.Command("xdg-open", url).Run()
	case "windows":
		err = exec.Command("cmd", "/c", "start", url).Run()
	}
	_ = err
}

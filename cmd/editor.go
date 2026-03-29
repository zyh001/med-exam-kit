package cmd

import (
	"fmt"
	"net"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
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
	host, _ := cmd.Flags().GetString("host")
	port, _ := cmd.Flags().GetInt("port")

	cfg := server.Config{
		Questions:     nil,
		Host:          host,
		Port:          port,
		RecordEnabled: false,
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
	switch os := os.Getenv("OSTYPE"); {
	case os == "darwin":
		err = exec.Command("open", url).Run()
	case os == "linux-gnu":
		err = exec.Command("xdg-open", url).Run()
	case os == "windows":
		err = exec.Command("cmd", "/c", "start", url).Run()
	}
	_ = err
}

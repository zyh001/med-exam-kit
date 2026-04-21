package cmd

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
)

var reloadCmd = &cobra.Command{
	Use:   "reload",
	Short: "向运行中的 med-exam 进程发送 SIGHUP，触发热重载（无需重启）",
	Long: `热重载配置，类似 nginx -s reload。

工作原理：
  1. 读取 PID 文件（默认 med-exam.pid，可通过 --pid-file 指定）
  2. 向该进程发送 SIGHUP 信号
  3. 运行中的服务收到信号后重读 med-exam-kit.yaml，
     重新加载题库文件，替换内存中的题库数据
  4. 所有正在进行的 HTTP 请求不受影响

示例：
  # 默认查找当前目录的 med-exam.pid
  med-exam reload

  # 指定 PID 文件
  med-exam reload --pid-file /var/run/med-exam.pid

  # 直接指定 PID
  med-exam reload --pid 12345
`,
	RunE: runReload,
}

var (
	reloadPidFile string
	reloadPid     int
)

func init() {
	rootCmd.AddCommand(reloadCmd)
	reloadCmd.Flags().StringVar(&reloadPidFile, "pid-file", "", "PID 文件路径（默认 med-exam.pid）")
	reloadCmd.Flags().IntVar(&reloadPid, "pid", 0, "直接指定进程 PID（跳过 PID 文件）")
}

func runReload(cmd *cobra.Command, args []string) error {
	pid := reloadPid

	if pid == 0 {
		// Read PID from file
		pidFile := reloadPidFile
		if pidFile == "" {
			pidFile = "med-exam.pid"
		}
		data, err := os.ReadFile(pidFile)
		if err != nil {
			return fmt.Errorf("读取 PID 文件失败 (%s): %w\n提示：请确认 med-exam quiz 正在运行，且 PID 文件存在", pidFile, err)
		}
		pidStr := strings.TrimSpace(string(data))
		pid, err = strconv.Atoi(pidStr)
		if err != nil || pid <= 0 {
			return fmt.Errorf("PID 文件内容无效: %q", pidStr)
		}
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("找不到进程 PID=%d: %w", pid, err)
	}

	if err := proc.Signal(syscall.SIGHUP); err != nil {
		if errors.Is(err, os.ErrProcessDone) {
			return fmt.Errorf("进程 PID=%d 已不存在", pid)
		}
		return fmt.Errorf("发送 SIGHUP 失败: %w", err)
	}

	fmt.Printf("✅ 已向进程 PID=%d 发送 SIGHUP，服务正在热重载题库...\n", pid)
	fmt.Println("   （重载完成后日志中会显示 \"热重载完成\"）")
	return nil
}

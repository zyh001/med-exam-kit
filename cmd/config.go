package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

// AppConfig mirrors the YAML config file structure.
type AppConfig struct {
	Host     string   `yaml:"host"`
	Port     int      `yaml:"port"`
	Pin      string   `yaml:"pin"`
	NoPin    bool     `yaml:"no_pin"`
	NoRecord bool     `yaml:"no_record"`
	Banks    []string `yaml:"banks"`
	Password string   `yaml:"password"`
	DB       string   `yaml:"db"`
	BankIDs  []int64  `yaml:"bank_ids"`
	// AI Q&A
	AIProvider      string `yaml:"ai_provider"`
	AIModel         string `yaml:"ai_model"`
	AIAPIKey        string `yaml:"ai_api_key"`
	AIBaseURL       string `yaml:"ai_base_url"`
	AIEnableThinking *bool `yaml:"ai_thinking"`
	// ASR (语音识别)
	ASRAPIKey  string `yaml:"asr_api_key"`
	ASRModel   string `yaml:"asr_model"`
	ASRBaseURL string `yaml:"asr_base_url"`
}

// defaultConfig returns a zero-value config with sensible defaults.
func defaultConfig() AppConfig {
	return AppConfig{
		Host: "127.0.0.1",
		Port: 5174,
	}
}

// loadConfig reads a flat YAML config file (no external deps).
// Format: key: value   # comment
// Lists:  key:
//
//	- item1
//	- item2
func loadConfig(path string) (AppConfig, error) {
	cfg := defaultConfig()

	f, err := os.Open(path)
	if err != nil {
		return cfg, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	var currentListKey string
	var bankIDList []int64
	var bankList []string

	for scanner.Scan() {
		line := scanner.Text()

		// Strip inline comments
		if idx := strings.Index(line, " #"); idx >= 0 {
			line = line[:idx]
		}
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// List item
		if strings.HasPrefix(line, "- ") {
			item := strings.TrimSpace(line[2:])
			switch currentListKey {
			case "banks":
				bankList = append(bankList, item)
			case "bank_ids":
				if id, err := strconv.ParseInt(item, 10, 64); err == nil {
					bankIDList = append(bankIDList, id)
				}
			}
			continue
		}

		// key: value
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])

		// If value is empty, might be a list header
		if val == "" {
			currentListKey = key
			continue
		}
		currentListKey = ""

		switch key {
		case "host":
			cfg.Host = val
		case "port":
			if p, err := strconv.Atoi(val); err == nil {
				cfg.Port = p
			}
		case "pin":
			cfg.Pin = val
		case "no_pin":
			cfg.NoPin = val == "true"
		case "no_record":
			cfg.NoRecord = val == "true"
		case "password":
			cfg.Password = val
		case "db":
			cfg.DB = val
		case "ai_provider":
			cfg.AIProvider = val
		case "ai_model":
			cfg.AIModel = val
		case "ai_api_key":
			cfg.AIAPIKey = val
		case "ai_base_url":
			cfg.AIBaseURL = val
		case "ai_thinking":
			if val == "true" {
				cfg.AIEnableThinking = boolPtr(true)
			} else if val == "false" {
				cfg.AIEnableThinking = boolPtr(false)
			}
		case "banks":
			// inline single value: banks: exam.mqb
			bankList = append(bankList, val)
		case "bank_ids":
			// inline: bank_ids: 1,2,3
			for _, part := range strings.Split(val, ",") {
				if id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64); err == nil {
					bankIDList = append(bankIDList, id)
				}
			}
		}
	}

	if len(bankList) > 0 {
		cfg.Banks = bankList
	}
	if len(bankIDList) > 0 {
		cfg.BankIDs = bankIDList
	}
	return cfg, scanner.Err()
}

// defaultConfigContent is the template written by `config init`.
const defaultConfigContent = `# med-exam-kit 配置文件
# 用法: med-exam-kit quiz --config med-exam-kit.yaml
# 或者将本文件命名为 med-exam-kit.yaml 放在工作目录，自动加载。

# ─── 服务器 ────────────────────────────────────────────────────────
host: 127.0.0.1        # 监听地址（公网访问改为 0.0.0.0）
port: 5174             # 监听端口

# ─── 访问码 ────────────────────────────────────────────────────────
pin:                   # 自定义访问码（留空则每次启动自动随机生成）
no_pin: false          # true = 完全禁用访问码（仅内网使用时安全）

# ─── 题库文件（本地 .mqb）─────────────────────────────────────────
# 可以写多行，每行一个题库路径
banks:
  - exam.mqb

password:              # .mqb 题库密码（所有题库共用，留空表示无密码）

# ─── 做题记录 ──────────────────────────────────────────────────────
no_record: false       # true = 禁用做题记录

# ─── PostgreSQL 数据库（可选，留空使用本地 SQLite）────────────────
# 优势：多用户共享、跨设备同步、支持统一管理多个题库
# 格式：postgres://用户名:密码@主机:端口/数据库名
db:                    # 例: postgres://med:secret@localhost:5432/medexam

# 从数据库加载的题库 ID（需先用 db import 导入）
# 如果同时填写了 banks 和 bank_ids，两者都会加载
bank_ids:
  # - 1
  # - 2

# ─── PostgreSQL 使用流程 ───────────────────────────────────────────
# 1. 导入题库:   med-exam-kit db import --dsn <DSN> exam.mqb
# 2. 查看题库:   med-exam-kit db status --dsn <DSN>
# 3. 迁移记录:   med-exam-kit db migrate-progress --dsn <DSN> --progress exam.progress.db
# 4. 在配置文件中填写 db 和 bank_ids，然后启动:
#               med-exam-kit quiz --config med-exam-kit.yaml

# --- AI 答疑（可选，配置后可对每道题进行 AI 深度解析）---------
ai_provider: deepseek  # openai / deepseek / ollama / qwen / kimi / minimax
ai_model:              # 留空使用 provider 默认模型
ai_api_key:            # API 密钥（必填，ollama 留空即可）
ai_base_url:           # 自定义 API 地址（留空使用 provider 默认地址）
ai_thinking:           # true=强制开启思考 / false=强制关闭 / 留空=自动检测
`

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "配置文件管理",
}

var configInitCmd = &cobra.Command{
	Use:   "init",
	Short: "生成默认配置文件 med-exam-kit.yaml",
	RunE: func(cmd *cobra.Command, args []string) error {
		outPath, _ := cmd.Flags().GetString("output")
		if _, err := os.Stat(outPath); err == nil {
			overwrite, _ := cmd.Flags().GetBool("force")
			if !overwrite {
				return fmt.Errorf("文件 %s 已存在，使用 --force 覆盖", outPath)
			}
		}
		if err := os.WriteFile(outPath, []byte(defaultConfigContent), 0644); err != nil {
			return fmt.Errorf("写入失败: %w", err)
		}
		fmt.Printf("✅ 已生成配置文件：%s\n", outPath)
		fmt.Printf("   请编辑后运行：med-exam-kit quiz --config %s\n", outPath)
		return nil
	},
}

func init() {
	configInitCmd.Flags().String("output", "med-exam-kit.yaml", "输出文件路径")
	configInitCmd.Flags().Bool("force", false, "覆盖已有配置文件")
	configCmd.AddCommand(configInitCmd)
	rootCmd.AddCommand(configCmd)
}

func boolPtr(v bool) *bool { return &v }

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
	// S3 图片存储（可选）
	S3Endpoint   string `yaml:"s3_endpoint"`
	S3Bucket     string `yaml:"s3_bucket"`
	S3AccessKey  string `yaml:"s3_access_key"`
	S3SecretKey  string `yaml:"s3_secret_key"`
	S3PublicBase string `yaml:"s3_public_base"`
	// 数据保留
	CleanupDays int `yaml:"cleanup_days"` // 不活跃用户数据保留天数，0=使用默认值7
	// AI 输出控制
	AIMaxTokens int `yaml:"ai_max_tokens"` // AI 单次回复最大 token 数，0=使用默认值2048
	// 日志
	LogFile  string `yaml:"log_file"`  // 日志文件路径，留空=只打印终端
	LogLevel string `yaml:"log_level"` // debug / info / warn / error，默认 info
	// 进程管理
	PidFile  string `yaml:"pid_file"`  // PID 文件路径，默认 med-exam.pid
	// 调试模式（勿用于生产）
	Debug bool `yaml:"debug"` // true 时暴露 /api/debug 与 /api/debug/exam-sessions 端点
	// 题库热重载监视器（stdlib 轮询，无需外部依赖）
	AutoReloadWatch    bool `yaml:"auto_reload_watch"`    // true 启用后台题库变更监视
	AutoReloadInterval int  `yaml:"auto_reload_interval"` // 轮询间隔秒数，0=默认 10
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
		case "asr_api_key":
			cfg.ASRAPIKey = val
		case "asr_model":
			cfg.ASRModel = val
		case "asr_base_url":
			cfg.ASRBaseURL = val
		case "s3_endpoint":
			cfg.S3Endpoint = val
		case "s3_bucket":
			cfg.S3Bucket = val
		case "s3_access_key":
			cfg.S3AccessKey = val
		case "s3_secret_key":
			cfg.S3SecretKey = val
		case "s3_public_base":
			cfg.S3PublicBase = val
		case "cleanup_days":
			if v, err := strconv.Atoi(val); err == nil && v > 0 {
				cfg.CleanupDays = v
			}
		case "ai_max_tokens":
			if v, err := strconv.Atoi(val); err == nil && v > 0 {
				cfg.AIMaxTokens = v
			}
		case "log_file":
			cfg.LogFile = val
		case "log_level":
			cfg.LogLevel = val
		case "pid_file":
			cfg.PidFile = val
		case "debug":
			cfg.Debug = val == "true"
		case "auto_reload_watch":
			cfg.AutoReloadWatch = val == "true"
		case "auto_reload_interval":
			if v, err := strconv.Atoi(val); err == nil && v > 0 {
				cfg.AutoReloadInterval = v
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

# --- ASR 语音识别（可选）----------------------------------------
asr_api_key:           # 语音识别 API 密钥
asr_model:             # 语音识别模型（留空使用默认）
asr_base_url:          # 自定义 ASR 地址

# ─── S3 图片对象存储（可选）──────────────────────────────────────
# 用于存储从题库外链下载的图片，解决图片跨域和外链失效问题。
# 使用步骤：
#   1. 启动 MinIO/RustFS：docker run -p 9000:9000 -p 9001:9001 minio/minio server /data --console-address ":9001"
#   2. 迁移图片：med-exam-kit img-migrate -b exam.mqb --endpoint http://localhost:9000 --bucket med-images --access-key minioadmin --secret-key minioadmin
#   3. 填写下方配置，重启服务后图片将通过 S3 加载
s3_endpoint:           # MinIO/RustFS 地址，例：http://localhost:9000
s3_bucket:             # 存储桶名称，例：med-images
s3_access_key:         # Access Key，例：minioadmin
s3_secret_key:         # Secret Key，例：minioadmin
s3_public_base:        # 公开访问 base URL（留空则使用 s3_endpoint/s3_bucket）

# ─── 日志（可选）────────────────────────────────────────────────
# 默认不写文件，只在终端打印。配置 log_file 后同时写入终端和文件。
# log_level: debug / info / warn / error（默认 info）
log_file:              # 日志文件路径，例：/var/log/med-exam-kit.log
log_level: info        # 日志级别
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

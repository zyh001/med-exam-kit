package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	aimod "github.com/zyh001/med-exam-kit/internal/ai"
)

var enrichCmd = &cobra.Command{
	Use:   "enrich",
	Short: "AI 补全题库：为缺答案/缺解析的小题自动生成内容",
	Long: `AI 补全题库：为缺答案/缺解析的小题自动生成内容

数据来源（二选一）：
  --bank          从已有 .mqb 题库文件读取
  -i/--input-dir  从 JSON 原始文件目录读取（自动去重）

输出规则：
  默认                    AI 结果存入 ai_answer/ai_discuss，另存为 *_ai.mqb
  --apply-ai              同时写入 answer/discuss 正式字段
  --apply-ai --in-place   就地覆盖原 .mqb 文件

模型示例：
  普通模型:  --model gpt-4o
  纯推理:    --model o3-mini / --model deepseek-reasoner
  混合思考:  --model qwen3-235b-a22b --thinking      (开启思考)
             --model qwen3-235b-a22b --no-thinking   (关闭思考，更快)`,
	RunE: runEnrich,
}

func init() {
	rootCmd.AddCommand(enrichCmd)
	f := enrichCmd.Flags()
	f.StringP("input", "i", "", "JSON 文件目录（与 --bank 二选一）")
	f.StringP("output", "o", "", "输出路径（.mqb），不填则自动命名 *_ai.mqb")
	f.String("provider", "", "AI provider: openai/deepseek/qwen/ollama/kimi/minimax")
	f.String("model", "", "模型名")
	f.String("api-key", "", "API Key（也可用环境变量 OPENAI_API_KEY）")
	f.String("base-url", "", "自定义 API Base URL")
	f.Int("max-workers", 0, "并发数（默认 4）")
	f.Bool("resume", true, "是否断点续跑")
	f.String("checkpoint-dir", "", "断点目录")
	f.StringSlice("mode", nil, "仅处理指定题型，如 A1型题")
	f.StringSlice("unit", nil, "仅处理包含关键词的章节")
	f.Int("limit", 0, "最多处理多少小题，0=不限制")
	f.Bool("dry-run", false, "仅预览待处理列表，不实际调用 AI")
	f.Bool("only-missing", true, "仅补缺失字段（默认）")
	f.Bool("force", false, "强制重新生成所有（与 --only-missing 互斥）")
	f.Bool("apply-ai", false, "将 AI 结果写入 answer/discuss 正式字段")
	f.Bool("in-place", false, "--bank 模式下就地修改原文件")
	f.Float64("timeout", 0, "AI 请求超时秒数（推理模型建议 180+，0=自动推断）")
	f.Bool("thinking", false, "混合思考模型开启深度思考")
	f.Bool("no-thinking", false, "混合思考模型关闭深度思考")
}

func runEnrich(cmd *cobra.Command, args []string) error {
	bankPath, _ := cmd.Root().PersistentFlags().GetString("bank")
	password, _ := cmd.Root().PersistentFlags().GetString("password")
	inputDir, _ := cmd.Flags().GetString("input")
	output, _ := cmd.Flags().GetString("output")
	provider, _ := cmd.Flags().GetString("provider")
	model, _ := cmd.Flags().GetString("model")
	apiKey, _ := cmd.Flags().GetString("api-key")
	baseURL, _ := cmd.Flags().GetString("base-url")
	maxWorkers, _ := cmd.Flags().GetInt("max-workers")
	resume, _ := cmd.Flags().GetBool("resume")
	ckptDir, _ := cmd.Flags().GetString("checkpoint-dir")
	modes, _ := cmd.Flags().GetStringSlice("mode")
	units, _ := cmd.Flags().GetStringSlice("unit")
	limit, _ := cmd.Flags().GetInt("limit")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	onlyMissing, _ := cmd.Flags().GetBool("only-missing")
	force, _ := cmd.Flags().GetBool("force")
	applyAI, _ := cmd.Flags().GetBool("apply-ai")
	inPlace, _ := cmd.Flags().GetBool("in-place")
	timeout, _ := cmd.Flags().GetFloat64("timeout")
	thinking, _ := cmd.Flags().GetBool("thinking")
	noThinking, _ := cmd.Flags().GetBool("no-thinking")

	if bankPath == "" && inputDir == "" {
		return fmt.Errorf("必须指定 --bank 或 -i/--input-dir 中的一个")
	}

	// Defaults
	if provider == "" {
		provider = "openai"
	}
	if model == "" {
		model = "gpt-4o"
	}
	if maxWorkers == 0 {
		maxWorkers = 4
	}
	if ckptDir == "" {
		ckptDir = "data/checkpoints"
	}
	if force {
		onlyMissing = false
	}

	// Resolve enable_thinking
	var enableThinking *bool
	if thinking {
		t := true
		enableThinking = &t
	} else if noThinking {
		f := false
		enableThinking = &f
	}

	// Model type detection & warnings
	pureR := aimod.IsReasoningModel(model)
	hybrid := aimod.IsHybridThinkingModel(model)

	if pureR {
		fmt.Printf("  🧠 纯推理模型: %s（始终开启深度思考）\n", model)
		if maxWorkers > 2 {
			fmt.Printf("  ⚠️  推理模型响应较慢，建议 --max-workers 1~2，当前: %d\n", maxWorkers)
		}
	} else if hybrid {
		state := "关闭（加 --thinking 可开启）"
		if thinking {
			state = "开启 🧠"
		}
		fmt.Printf("  🔀 混合思考模型: %s  深度思考: %s\n", model, state)
		if thinking && maxWorkers > 2 {
			fmt.Printf("  ⚠️  思考模式响应较慢，建议 --max-workers 1~2，当前: %d\n", maxWorkers)
		}
	}

	// Auto timeout
	useSlow := pureR || (hybrid && thinking)
	if timeout == 0 {
		if useSlow {
			timeout = 180
		} else {
			timeout = 60
		}
	}

	// Resolve in-place
	if inPlace && bankPath != "" {
		output = bankPath
		fmt.Printf("  ⚠️  --in-place：将就地修改原文件 %s\n", output)
	}

	// Try to read api-key from env if not set
	if apiKey == "" {
		apiKey = os.Getenv("OPENAI_API_KEY")
	}

	cfg := aimod.EnricherConfig{
		BankPath:       bankPath,
		OutputPath:     output,
		InputDir:       inputDir,
		Password:       password,
		Provider:       provider,
		Model:          model,
		APIKey:         apiKey,
		BaseURL:        baseURL,
		MaxWorkers:     maxWorkers,
		Resume:         resume,
		CheckpointDir:  ckptDir,
		ModesFilter:    modes,
		ChaptersFilter: units,
		Limit:          limit,
		DryRun:         dryRun,
		OnlyMissing:    onlyMissing,
		ApplyAI:        applyAI,
		InPlace:        inPlace,
		Timeout:        timeout,
		EnableThinking: enableThinking,
	}

	enricher := aimod.NewEnricher(cfg)
	return enricher.Run()
}

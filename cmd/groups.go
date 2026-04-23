package cmd

import (
	"github.com/spf13/cobra"
)

// serveCmd / bankCmd 是两个分组父命令，把原先零散的顶层子命令按功能聚到一起：
//
//   med-exam serve quiz       刷题 Web 服务器
//   med-exam serve editor     题库编辑器 Web 服务器
//   med-exam bank build       从目录 / JSON 构建 .mqb
//   med-exam bank info        查看题库元数据
//   med-exam bank inspect     按指纹 / 条件检查题目
//   med-exam bank export      导出题库为 JSON
//   med-exam bank enrich      AI 补充题目解析
//   med-exam bank generate    AI 生成新题
//   med-exam bank reindex     重建 fingerprint 索引
//   med-exam bank migrate-images   迁移图片到 S3 / 本地存储
//
// db / config / reload 保持原样（它们本来就是独立领域）。
//
// 旧的顶层命令名（quiz / editor / build / info / ...）继续**可见**在 `--help`
// 列表中，Short 里附带一个 "→ serve/bank ..." 的指示，让熟悉老命令的用户
// 无缝过渡，同时也能发现新的分组写法。真实的 flag/RunE 实现挂在分组下的那一份；
// 顶层的只是一个薄转发别名。
var (
	serveCmd = &cobra.Command{
		Use:   "serve",
		Short: "启动 Web 服务（quiz 刷题 / editor 编辑器）",
		Long: `启动 Web 服务：
  serve quiz    启动刷题 Web 服务器
  serve editor  启动题库编辑器 Web 服务器`,
	}
	bankCmd = &cobra.Command{
		Use:   "bank",
		Short: "题库管理（构建 / 查询 / 导入导出 / AI 生成）",
		Long: `题库管理工具集：
  bank build           从目录或 JSON 构建 .mqb 题库
  bank info            查看题库元数据
  bank inspect         按指纹 / 条件检查题目详情
  bank export          导出题库为 JSON
  bank enrich          AI 补充题目解析
  bank generate        AI 生成新题
  bank reindex         重建 fingerprint 索引
  bank migrate-images  迁移图片到 S3 / 本地存储`,
	}
)

// installGroupedCommands 在 Execute 阶段（所有 init() 都已把子命令挂到 rootCmd 之后）
// 把指定的顶层命令搬到 serve / bank 下，然后回灌一个**可见的**同名顶层别名，
// 保证 `med-exam quiz ...`、`med-exam build ...` 这类旧用法在 `--help` 中依然
// 看得到，只是 Short 描述里会多一行"→ serve quiz"之类的分组指示。
func installGroupedCommands() {
	rootCmd.AddCommand(serveCmd, bankCmd)

	type target struct {
		parent  *cobra.Command
		newName string // 非空表示在分组里重命名（顶层别名仍用旧名）
	}
	toRegroup := map[string]target{
		"quiz":        {serveCmd, ""},
		"editor":      {serveCmd, ""},
		"build":       {bankCmd, ""},
		"info":        {bankCmd, ""},
		"inspect":     {bankCmd, ""},
		"export":      {bankCmd, ""},
		"enrich":      {bankCmd, ""},
		"generate":    {bankCmd, ""},
		"reindex":     {bankCmd, ""},
		"img-migrate": {bankCmd, "migrate-images"},
	}

	// 捕获当前顶层命令的快照（排除刚加的两个分组）。
	existing := map[string]*cobra.Command{}
	for _, c := range rootCmd.Commands() {
		if c == serveCmd || c == bankCmd {
			continue
		}
		existing[c.Name()] = c
	}

	for name, tgt := range toRegroup {
		c, ok := existing[name]
		if !ok {
			continue
		}
		// 旧命令先从 root 卸下（稍后会以别名形式再加回来）
		rootCmd.RemoveCommand(c)

		// 保留一个顶层别名：浅拷贝 Command，flag/RunE 共享底层实现。
		// 因为 cobra.Command 的私有字段（parent / subcommands 列表）会在后续
		// AddCommand 时被重新设置，所以浅拷贝安全；FlagSet 虽然共享，一次进程
		// 只跑一条路径，不会互相污染。
		aliasCopy := *c
		alias := &aliasCopy
		canon := name
		if tgt.newName != "" {
			canon = tgt.newName
		}
		// 可见别名，Short 里加一个箭头指向分组后的正式路径，引导用户过渡。
		alias.Hidden = false
		alias.Short = c.Short + "  (= " + tgt.parent.Name() + " " + canon + ")"
		rootCmd.AddCommand(alias)

		// 把真正的命令挂到分组下；如需重命名则改 Use。
		if tgt.newName != "" {
			c.Use = tgt.newName
		}
		tgt.parent.AddCommand(c)
	}
}

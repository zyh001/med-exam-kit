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
// 旧的顶层命令名（quiz / editor / build / info / ...）仍然可用，
// 但标记为 Hidden=true，不在 `--help` 中展示，仅供过去的脚本 / 文档向后兼容。
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
// 把指定的顶层命令搬到 serve / bank 下，并在 rootCmd 保留一个隐藏的同名别名，
// 以保证 `med-exam quiz ...`、`med-exam build ...` 这类旧用法继续生效。
func installGroupedCommands() {
	rootCmd.AddCommand(serveCmd, bankCmd)

	type target struct {
		parent  *cobra.Command
		newName string // 非空表示在分组里重命名（旧名仍然作为顶层 Hidden 别名保留）
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
		// 旧命令从 root 卸下
		rootCmd.RemoveCommand(c)

		// 保留一个隐藏的旧名别名，底层逻辑复用同一份 RunE/Flags：浅拷贝能行，
		// 因为 cobra Command 的私有字段（parent/subcommands 列表）会在后续
		// AddCommand 时被重新设置；FlagSet 虽然共享，但一次进程里只跑一条路径，
		// 不会互相污染。
		aliasCopy := *c
		alias := &aliasCopy
		canon := name
		if tgt.newName != "" {
			canon = tgt.newName
		}
		alias.Hidden = true
		alias.Short = "（已弃用，请使用 `" + tgt.parent.Name() + " " + canon + "`）"
		rootCmd.AddCommand(alias)

		// 把真命令挂到分组下；如需重命名则改 Use。
		if tgt.newName != "" {
			c.Use = tgt.newName
		}
		tgt.parent.AddCommand(c)
	}
}

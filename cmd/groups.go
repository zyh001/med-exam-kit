package cmd

import (
	"github.com/spf13/cobra"
)

// ── CLI 分组结构（激进版：覆盖全部命令）────────────────────────────
//
// 设计目标：`med-exam --help` 只显示父命令，避免 17 个扁平命令把新用户劝退。
// 老命令名保留（Hidden=true），旧脚本不改也能跑，但从 `--help` 消失。
//
//   med-exam serve quiz            刷题 Web 服务器
//   med-exam serve editor          题库编辑器 Web 服务器
//
//   med-exam bank build            从目录/JSON 构建 .mqb 题库
//   med-exam bank info             查看题库元数据
//   med-exam bank inspect          按指纹/条件检查题目
//   med-exam bank export           导出题库为 JSON/xlsx/...
//   med-exam bank enrich           AI 补充题目解析
//   med-exam bank generate         AI 生成新题
//   med-exam bank reindex          重算题库指纹索引
//   med-exam bank migrate-images   迁移图片到 S3
//
//   med-exam db import             导入 .mqb 到 Postgres
//   med-exam db status             查看数据库中的题库
//   med-exam db delete             从 PG 删除题库
//   med-exam db migrate-progress   将 SQLite 进度迁移到 PG
//   med-exam db repair             修复旧数据
//
//   med-exam admin reload          向运行中的进程发 SIGHUP
//   med-exam admin config init     生成示例配置文件
//
//   med-exam completion            shell 补全（cobra 标准）
//
// 老命令映射（Hidden=true 兼容运行）：
//   quiz          → serve quiz
//   editor        → serve editor
//   build         → bank build
//   info          → bank info
//   inspect       → bank inspect
//   export        → bank export
//   enrich        → bank enrich
//   generate      → bank generate
//   reindex       → bank reindex
//   img-migrate   → bank migrate-images
//   reload        → admin reload
//   config        → admin config

var (
	serveCmd = &cobra.Command{
		Use:   "serve",
		Short: "启动 Web 服务（quiz 刷题 / editor 编辑器）",
		Long: `启动 Web 服务：

  serve quiz     启动刷题 Web 服务器
  serve editor   启动题库编辑器 Web 服务器

示例：
  med-exam serve quiz -b exam.mqb
  med-exam serve editor -b exam.mqb`,
	}
	bankCmd = &cobra.Command{
		Use:   "bank",
		Short: "题库管理（构建 / 查询 / 导入导出 / AI 生成）",
		Long: `题库管理工具集（操作本地 .mqb 文件）：

  bank build           从目录或 JSON 构建 .mqb 题库
  bank info            查看题库元数据
  bank inspect         按指纹/条件检查题目详情
  bank export          导出题库到 xlsx/csv/docx/pdf/json
  bank enrich          AI 补全题库：为缺答案/缺解析的题自动生成
  bank generate        AI 自动组卷 → 导出 Word 试卷
  bank reindex         重算题库内所有指纹
  bank migrate-images  扫描外链图片，下载并上传到 S3`,
	}
	adminCmd = &cobra.Command{
		Use:   "admin",
		Short: "运维管理（热重载 / 配置文件）",
		Long: `运维管理工具集：

  admin reload        向运行中的 med-exam 进程发送 SIGHUP，触发题库热重载
  admin config init   在当前目录生成示例配置文件 med-exam-kit.yaml

若要修改 PostgreSQL 数据，请用 'med-exam db ...' 子命令。`,
	}
)

// installGroupedCommands 是 CLI 重组的入口。
//
// 工作顺序：
//   1. 先 snapshot 所有由各子命令 init() 注入到 rootCmd 上的顶层命令
//   2. 依次把这些顶层命令搬到 serve / bank / admin 下（db 已经是分组，保留原样）
//   3. 在 rootCmd 挂一个 Hidden=true 的同名别名（浅拷贝 Command 即可，
//      flag/RunE 共享同一份实现），保证老脚本 `med-exam quiz -b ...`、
//      `med-exam config init` 等继续能跑
//
// 注意：该函数由 Execute() 调用，此时所有 init() 都已执行完，rootCmd 上
// 所有子命令都挂载好了，可以安全地读取 rootCmd.Commands() 做 snapshot。
func installGroupedCommands() {
	rootCmd.AddCommand(serveCmd, bankCmd, adminCmd)

	type target struct {
		parent  *cobra.Command
		newName string // 非空时在分组下改名（顶层 Hidden 别名仍用旧名）
	}
	regroup := map[string]target{
		// serve
		"quiz":   {serveCmd, ""},
		"editor": {serveCmd, ""},
		// bank
		"build":       {bankCmd, ""},
		"info":        {bankCmd, ""},
		"inspect":     {bankCmd, ""},
		"export":      {bankCmd, ""},
		"enrich":      {bankCmd, ""},
		"generate":    {bankCmd, ""},
		"reindex":     {bankCmd, ""},
		"img-migrate": {bankCmd, "migrate-images"},
		// admin
		"reload": {adminCmd, ""},
		"config": {adminCmd, ""},
	}

	// snapshot 现有顶层命令（排除刚加的三个分组）
	existing := map[string]*cobra.Command{}
	for _, c := range rootCmd.Commands() {
		if c == serveCmd || c == bankCmd || c == adminCmd {
			continue
		}
		existing[c.Name()] = c
	}

	for oldName, tgt := range regroup {
		c, ok := existing[oldName]
		if !ok {
			continue
		}
		// 旧命令从 root 卸下
		rootCmd.RemoveCommand(c)

		// Hidden 别名：浅拷贝，共享 Flags/RunE
		//
		// 为什么浅拷贝安全：cobra.Command 私有字段（parent, subcommands 列表、
		// flag 继承缓存等）会在 AddCommand 时被重置；FlagSet 虽然由指针共享，
		// 但一次进程里用户只执行一条命令路径，两份不会同时被解析。
		aliasCopy := *c
		alias := &aliasCopy
		alias.Hidden = true
		canon := oldName
		if tgt.newName != "" {
			canon = tgt.newName
		}
		deprLine := "[此命令已迁移到 `" + tgt.parent.Name() + " " + canon + "`，" +
			"旧名仍可使用但不再出现在 --help 中。]\n\n"
		if alias.Long != "" {
			alias.Long = deprLine + alias.Long
		} else {
			alias.Long = deprLine + alias.Short
		}
		rootCmd.AddCommand(alias)

		// 把真命令挂到分组下；如需在分组里重命名则改 Use
		if tgt.newName != "" {
			c.Use = tgt.newName
		}
		tgt.parent.AddCommand(c)
	}
}

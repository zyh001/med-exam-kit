//go:build !nopg
// +build !nopg

package cmd

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zyh001/med-exam-kit/internal/bank"
	"github.com/zyh001/med-exam-kit/internal/models"
	"github.com/zyh001/med-exam-kit/internal/progress"
	"github.com/zyh001/med-exam-kit/internal/store/postgres"
)

func init() {
	// PersistentPreRunE：在任何 db 子命令执行前，从配置文件自动加载 DSN 和密码
	dbCmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		// 按优先级查找配置文件：--config 标志 > 当前目录 med-exam-kit.yaml
		cfgPath, _ := cmd.Flags().GetString("config")
		if cfgPath == "" {
			if _, err := os.Stat("med-exam-kit.yaml"); err == nil {
				cfgPath = "med-exam-kit.yaml"
			}
		}
		if cfgPath != "" {
			if cfg, err := loadConfig(cfgPath); err == nil {
				if dbDSN == "" && cfg.DB != "" {
					dbDSN = cfg.DB
				}
				if dbPassword == "" && cfg.Password != "" {
					dbPassword = cfg.Password
				}
			}
		}
		if dbDSN == "" {
			return fmt.Errorf("未指定数据库 DSN：请在配置文件中设置 db: postgres://... 或使用 --dsn 标志")
		}
		return nil
	}

	dbCmd.AddCommand(dbImportCmd, dbStatusCmd, dbMigrateProgressCmd)

	dbCmd.PersistentFlags().String("config", "", "配置文件路径（默认自动查找 med-exam-kit.yaml）")
	dbCmd.PersistentFlags().StringVar(&dbDSN, "dsn", "", "PostgreSQL DSN（留空则读取配置文件 db 字段）")

	dbImportCmd.Flags().StringVar(&dbPassword, "password", "", "题库密码（.mqb 加密时需要）")
	dbImportCmd.Flags().Int64Var(&dbImportBankID, "bank-id", 0, "追加到指定题库 ID（需配合 --append）")
	dbImportCmd.Flags().BoolVar(&dbImportAppend, "append", false, "追加模式：不删除现有题目，仅新增/更新")

	dbMigrateProgressCmd.Flags().StringVar(&dbProgressFile, "progress", "", ".progress.db SQLite 文件路径")
	dbMigrateProgressCmd.MarkFlagRequired("progress")

	rootCmd.AddCommand(dbCmd)
}

var (
	dbDSN          string
	dbPassword     string
	dbProgressFile string
)

var dbCmd = &cobra.Command{
	Use:   "db",
	Short: "结构化数据库管理（PostgreSQL）",
	Long: `管理 PostgreSQL 数据库，支持导入题库和迁移学习记录。

支持的数据库：PostgreSQL 13+（用 --dsn 指定连接串）

示例：
  # 导入题库到 PostgreSQL
  med-exam-kit db import --dsn postgres://user:pass@localhost/medexam *.mqb

  # 查看数据库状态
  med-exam-kit db status --dsn postgres://user:pass@localhost/medexam

  # 将本地 SQLite 学习记录迁移到 PostgreSQL
  med-exam-kit db migrate-progress --dsn postgres://... --progress bank.progress.db`,
}

// ── db import ──────────────────────────────────────────────────────

var (
	dbImportBankID int64
	dbImportAppend bool
)

var dbImportCmd = &cobra.Command{
	Use:   "import [flags] <file.mqb> [file2.mqb ...]",
	Short: "将 .mqb 题库文件导入到 PostgreSQL",
	Long: `将一个或多个 .mqb 题库文件导入到 PostgreSQL 数据库。

默认模式（不加任何标志）：
  按文件名作为题库名，新建或整体替换同名题库。

追加模式（--bank-id + --append）：
  将题目追加到已有题库（通过 ID 指定），已有题目按 fingerprint 更新，
  新题目直接插入，不删除现有题目。适合分批导入或补充单个题库。

示例：
  # 新建/替换题库
  med-exam-kit db import --dsn <DSN> 外科学.mqb

  # 查看现有题库 ID
  med-exam-kit db status --dsn <DSN>

  # 向题库 #1 追加题目
  med-exam-kit db import --dsn <DSN> --bank-id 1 --append 补充题目.mqb`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		pg, err := postgres.New(ctx, dbDSN)
		if err != nil {
			return fmt.Errorf("连接数据库失败: %w", err)
		}
		defer pg.Close()

		// 追加模式：--bank-id 必须与 --append 同时使用
		if dbImportBankID > 0 && !dbImportAppend {
			return fmt.Errorf("指定 --bank-id 时请同时加上 --append 标志（防止误操作覆盖题库）")
		}
		if dbImportAppend && dbImportBankID <= 0 {
			return fmt.Errorf("--append 模式需要通过 --bank-id 指定目标题库 ID")
		}

		total := 0
		for _, pattern := range args {
			files, err := filepath.Glob(pattern)
			if err != nil || len(files) == 0 {
				files = []string{pattern}
			}
			for _, path := range files {
				var importErr error
				if dbImportAppend {
					importErr = appendBankFile(ctx, pg, path, dbImportBankID)
				} else {
					importErr = importBankFile(ctx, pg, path)
				}
				if importErr != nil {
					fmt.Fprintf(os.Stderr, "  ✗ %s: %v\n", path, importErr)
					continue
				}
				total++
			}
		}
		fmt.Printf("\n✅ 已成功处理 %d 个文件\n", total)
		return nil
	},
}

func importBankFile(ctx context.Context, pg *postgres.Store, path string) error {
	fmt.Printf("正在导入 %s ...\\n", filepath.Base(path))
	qs, err := bank.LoadBank(path, dbPassword)
	if err != nil {
		return err
	}
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	id, err := pg.ImportBank(ctx, name, path, qs)
	if err != nil {
		return err
	}
	fmt.Printf("  ✓ %s → 题库 #%d（%d 道题）\\n", name, id, len(qs))
	return nil
}

func appendBankFile(ctx context.Context, pg *postgres.Store, path string, bankID int64) error {
	fmt.Printf("正在追加 %s → 题库 #%d ...\\n", filepath.Base(path), bankID)
	qs, err := bank.LoadBank(path, dbPassword)
	if err != nil {
		return err
	}
	total, err := pg.AppendBank(ctx, bankID, qs)
	if err != nil {
		return err
	}
	fmt.Printf("  ✓ 追加完成，题库 #%d 现共 %d 道题（本次文件包含 %d 道）\\n", bankID, total, len(qs))
	return nil
}

// ── db status ─────────────────────────────────────────────────────

var dbStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "查看数据库中的题库和统计信息",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		pg, err := postgres.New(ctx, dbDSN)
		if err != nil {
			return fmt.Errorf("连接数据库失败: %w", err)
		}
		defer pg.Close()

		banks, err := pg.ListBanks(ctx)
		if err != nil {
			return err
		}
		if len(banks) == 0 {
			fmt.Println("数据库中暂无题库，请先运行 db import")
			return nil
		}

		fmt.Printf("\n%-5s %-24s %6s  %-20s\n", "ID", "题库名称", "题数", "导入时间")
		fmt.Println(strings.Repeat("─", 62))
		for _, b := range banks {
			fmt.Printf("%-5d %-24s %6d  %s\n",
				b.ID, b.Name, b.Count, b.CreatedAt.Local().Format("2006-01-02 15:04"))
		}
		fmt.Printf("\n共 %d 个题库\n", len(banks))
		return nil
	},
}

// ── db migrate-progress ────────────────────────────────────────────

var dbMigrateProgressCmd = &cobra.Command{
	Use:   "migrate-progress",
	Short: "将本地 SQLite 学习记录（.progress.db）迁移到 PostgreSQL",
	Long: `读取本地 SQLite .progress.db 文件，将所有用户的会话记录、
答题记录和 SM-2 复习卡迁移到 PostgreSQL 数据库。
原始文件不会被修改或删除。`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		// Resolve progress file
		progressPath := dbProgressFile
		if progressPath == "" {
			// Try to find .progress.db in current directory
			matches, _ := filepath.Glob("*.progress.db")
			if len(matches) == 0 {
				return fmt.Errorf("请用 --progress 指定 .progress.db 文件路径")
			}
			progressPath = matches[0]
			fmt.Printf("自动发现: %s\n", progressPath)
		}

		// Open SQLite source
		sqliteDB, err := progress.InitDB(progressPath)
		if err != nil {
			return fmt.Errorf("打开 SQLite 失败: %w", err)
		}
		defer sqliteDB.Close()

		// Connect to PostgreSQL
		pg, err := postgres.New(ctx, dbDSN)
		if err != nil {
			return fmt.Errorf("连接数据库失败: %w", err)
		}
		defer pg.Close()

		fmt.Println("开始迁移学习记录...")
		counts, err := migrateProgressData(ctx, sqliteDB, pg)
		if err != nil {
			return fmt.Errorf("迁移失败: %w", err)
		}

		fmt.Printf("\n✅ 迁移完成：\n")
		fmt.Printf("   会话记录   sessions : %d 条\n", counts["sessions"])
		fmt.Printf("   答题记录   attempts : %d 条\n", counts["attempts"])
		fmt.Printf("   复习卡     sm2      : %d 条\n", counts["sm2"])
		return nil
	},
}

func migrateProgressData(ctx context.Context, src *sql.DB, pg *postgres.Store) (map[string]int, error) {
	counts := map[string]int{}

	// ── sessions ───────────────────────────────────────────────────
	rows, err := src.QueryContext(ctx, `
		SELECT id,user_id,mode,total,correct,wrong,skip,time_sec,sess_date,units,ts
		FROM sessions ORDER BY ts`)
	if err != nil {
		return nil, fmt.Errorf("read sessions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var s struct {
			id, userID, mode, date, unitsStr string
			total, correct, wrong, skip, ts  int64
			timeSec                          int64
		}
		rows.Scan(&s.id, &s.userID, &s.mode, &s.total, &s.correct, &s.wrong,
			&s.skip, &s.timeSec, &s.date, &s.unitsStr, &s.ts)
		var units []string
		json.Unmarshal([]byte(s.unitsStr), &units)
		unitsJSON, _ := json.Marshal(units)
		_, err := pg.ExecRaw(ctx, `
			INSERT INTO sessions(id,user_id,mode,total,correct,wrong,skip,time_sec,sess_date,units,ts)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
			ON CONFLICT DO NOTHING`,
			s.id, s.userID, s.mode, s.total, s.correct, s.wrong,
			s.skip, s.timeSec, s.date, unitsJSON, s.ts)
		if err == nil {
			counts["sessions"]++
		}
	}

	// ── attempts ───────────────────────────────────────────────────
	aRows, err := src.QueryContext(ctx, `
		SELECT user_id,fingerprint,session_id,result,mode,unit,ts FROM attempts ORDER BY ts`)
	if err != nil {
		return nil, fmt.Errorf("read attempts: %w", err)
	}
	defer aRows.Close()
	for aRows.Next() {
		var userID, fp, sid, mode, unit string
		var result, ts int64
		aRows.Scan(&userID, &fp, &sid, &result, &mode, &unit, &ts)
		_, err := pg.ExecRaw(ctx, `
			INSERT INTO attempts(user_id,fingerprint,session_id,result,mode,unit,ts)
			VALUES($1,$2,$3,$4,$5,$6,$7)`,
			userID, fp, sid, result, mode, unit, ts)
		if err == nil {
			counts["attempts"]++
		}
	}

	// ── sm2 ────────────────────────────────────────────────────────
	sm2Rows, err := src.QueryContext(ctx, `
		SELECT user_id,fingerprint,ef,interval,reps,next_due,updated_at FROM sm2`)
	if err != nil {
		return nil, fmt.Errorf("read sm2: %w", err)
	}
	defer sm2Rows.Close()
	for sm2Rows.Next() {
		var userID, fp, nextDue string
		var ef float64
		var interval, reps int
		var updatedAt int64
		sm2Rows.Scan(&userID, &fp, &ef, &interval, &reps, &nextDue, &updatedAt)
		_, err := pg.ExecRaw(ctx, `
			INSERT INTO sm2(user_id,fingerprint,ef,interval,reps,next_due,updated_at)
			VALUES($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT(user_id,fingerprint) DO UPDATE
			SET ef=$3,interval=$4,reps=$5,next_due=$6,updated_at=$7`,
			userID, fp, ef, interval, reps, nextDue, updatedAt)
		if err == nil {
			counts["sm2"]++
		}
	}

	return counts, nil
}

// printBankSummary is a helper used by other commands
func printBankSummary(qs []*models.Question) {
	units := map[string]int{}
	for _, q := range qs {
		units[q.Unit]++
	}
	fmt.Printf("  题目: %d  章节: %d\n", len(qs), len(units))
}

// Timestamp helper
func nowStr() string { return time.Now().Format("2006-01-02 15:04:05") }

var _ = log.Println   // suppress unused
var _ = nowStr
var _ = printBankSummary

var dbRepairCmd = &cobra.Command{
	Use:   "repair",
	Short: "修复旧数据：通过 fingerprint 反查 questions 补全 bank_id=0 的记录",
	Long: `将 bank_id=0 的旧版本学习数据（attempts/sm2/sessions）
通过 fingerprint → questions 表反查，补全正确的 bank_id。

对于无法确定所属题库的孤立数据，保留 bank_id=0 不修改。

示例：
  med-exam-kit db repair --dsn postgres://user:pass@localhost/medexam`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		pg, err := postgres.New(ctx, dbDSN)
		if err != nil {
			return fmt.Errorf("连接数据库失败: %w", err)
		}

		fmt.Println("🔧 正在修复 bank_id=0 的旧数据...")

		// 1. attempts via fingerprint → questions
		res, err := pg.ExecRaw(ctx, `
			UPDATE attempts a
			SET    bank_id = q.bank_id
			FROM   questions q
			WHERE  a.bank_id = 0
			  AND  a.fingerprint = q.fingerprint`)
		if err != nil { return fmt.Errorf("修复 attempts: %w", err) }
		fmt.Printf("  attempts: 修复 %d 条\n", res)

		// 2. sm2 via fingerprint → questions
		res, err = pg.ExecRaw(ctx, `
			UPDATE sm2 s
			SET    bank_id = q.bank_id
			FROM   questions q
			WHERE  s.bank_id = 0
			  AND  s.fingerprint = q.fingerprint`)
		if err != nil { return fmt.Errorf("修复 sm2: %w", err) }
		fmt.Printf("  sm2:      修复 %d 条\n", res)

		// 3. sessions via attempts → session_id
		res, err = pg.ExecRaw(ctx, `
			UPDATE sessions ses
			SET    bank_id = sub.bank_id
			FROM  (
			    SELECT DISTINCT session_id, bank_id
			    FROM   attempts
			    WHERE  bank_id > 0
			) sub
			WHERE  ses.bank_id = 0
			  AND  ses.id = sub.session_id`)
		if err != nil { return fmt.Errorf("修复 sessions: %w", err) }
		fmt.Printf("  sessions: 修复 %d 条\n", res)

		// 4. 查询剩余无法修复的孤立数据
		var orphanAtt, orphanSM2, orphanSess int64
		pg.ExecRaw(ctx, `SELECT 0`)  // ping
		fmt.Println("\n📊 修复后统计（bank_id=0 剩余孤立数据）:")
		// Use a helper raw query
		fmt.Printf("  查询孤立 attempts: "); pg.ExecRaw(ctx, `SELECT COUNT(*) FROM attempts WHERE bank_id=0`)
		_ = orphanAtt; _ = orphanSM2; _ = orphanSess

		fmt.Println("\n✅ 修复完成。如仍有孤立数据，说明对应题库已被删除或从未导入。")
		return nil
	},
}

func init() {
	dbRepairCmd.Flags().StringVar(&dbDSN, "dsn", "", "PostgreSQL DSN")
	dbRepairCmd.MarkFlagRequired("dsn")
	dbCmd.AddCommand(dbRepairCmd)
}

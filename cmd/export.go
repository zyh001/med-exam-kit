package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/med-exam-kit/med-exam-kit/internal/bank"
	"github.com/med-exam-kit/med-exam-kit/internal/exporters"
	"github.com/med-exam-kit/med-exam-kit/internal/filters"
	"github.com/spf13/cobra"
)

var exportCmd = &cobra.Command{
	Use:   "export",
	Short: "导出题库到 xlsx/csv/docx/pdf/json/db",
	RunE:  runExport,
}

func init() {
	rootCmd.AddCommand(exportCmd)
	exportCmd.Flags().StringP("output-dir", "o", "data/output", "输出目录")
	exportCmd.Flags().StringSliceP("format", "f", []string{"xlsx"}, "导出格式：xlsx,csv,docx,pdf,json,db")
	exportCmd.Flags().Bool("split-options", false, "拆分选项为独立列（xlsx/csv）")
	exportCmd.Flags().StringSlice("mode", nil, "题型过滤")
	exportCmd.Flags().StringSlice("unit", nil, "章节关键词过滤")
	exportCmd.Flags().StringSlice("pkg", nil, "来源过滤")
	exportCmd.Flags().String("keyword", "", "题干关键词")
	exportCmd.Flags().Int("min-rate", 0, "最低正确率")
	exportCmd.Flags().Int("max-rate", 100, "最高正确率")
}

func runExport(cmd *cobra.Command, args []string) error {
	bankPath, _ := cmd.Root().PersistentFlags().GetString("bank")
	password, _ := cmd.Root().PersistentFlags().GetString("password")
	outDir, _ := cmd.Flags().GetString("output-dir")
	formats, _ := cmd.Flags().GetStringSlice("format")
	splitOpts, _ := cmd.Flags().GetBool("split-options")
	modes, _ := cmd.Flags().GetStringSlice("mode")
	units, _ := cmd.Flags().GetStringSlice("unit")
	pkgs, _ := cmd.Flags().GetStringSlice("pkg")
	keyword, _ := cmd.Flags().GetString("keyword")
	minRate, _ := cmd.Flags().GetInt("min-rate")
	maxRate, _ := cmd.Flags().GetInt("max-rate")

	if bankPath == "" {
		return fmt.Errorf("请用 -b 指定题库路径")
	}

	fmt.Printf("📂 加载题库：%s\n", bankPath)
	questions, err := bank.LoadBank(bankPath, password)
	if err != nil {
		return err
	}
	fmt.Printf("   共 %d 道题\n", len(questions))

	// Apply filters
	crit := filters.FilterCriteria{
		Modes: modes, Units: units, Pkgs: pkgs,
		Keyword: keyword, MinRate: minRate, MaxRate: maxRate,
	}
	if maxRate == 0 {
		crit.MaxRate = 100
	}
	questions = filters.Apply(questions, crit)
	fmt.Printf("   过滤后 %d 道题\n", len(questions))

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}

	base := filepath.Join(outDir, "export")
	for _, fmt_ := range formats {
		switch strings.ToLower(strings.TrimSpace(fmt_)) {
		case "csv":
			out := base + ".csv"
			if err := exporters.ExportCSV(questions, out, splitOpts); err != nil {
				fmt.Fprintf(os.Stderr, "  ⚠ CSV 导出失败：%v\n", err)
			} else {
				fmt.Printf("  ✅ CSV → %s\n", out)
			}
		case "xlsx":
			out := base + ".xlsx"
			if err := exporters.ExportXLSX(questions, out, splitOpts); err != nil {
				fmt.Fprintf(os.Stderr, "  ⚠ XLSX 导出失败：%v\n", err)
			} else {
				fmt.Printf("  ✅ XLSX → %s\n", out)
			}
		case "json":
			out := base + ".json"
			if err := exporters.ExportJSON(questions, out); err != nil {
				fmt.Fprintf(os.Stderr, "  ⚠ JSON 导出失败：%v\n", err)
			} else {
				fmt.Printf("  ✅ JSON → %s\n", out)
			}
		case "db":
			out := base + ".db"
			if err := exporters.ExportDB(questions, out); err != nil {
				fmt.Fprintf(os.Stderr, "  ⚠ DB 导出失败：%v\n", err)
			} else {
				fmt.Printf("  ✅ SQLite → %s\n", out)
			}
		case "docx":
			out := base + ".docx"
			if err := exporters.ExportDOCX(questions, out); err != nil {
				fmt.Fprintf(os.Stderr, "  ⚠ DOCX 导出失败：%v\n", err)
			} else {
				fmt.Printf("  ✅ DOCX → %s\n", out)
			}
		case "pdf":
			out := base + ".pdf"
			if err := exporters.ExportPDF(questions, out); err != nil {
				fmt.Fprintf(os.Stderr, "  ⚠ PDF 导出失败：%v\n", err)
			} else {
				fmt.Printf("  ✅ PDF → %s\n", out)
			}
		default:
			fmt.Fprintf(os.Stderr, "  ⚠ 未知格式：%s\n", fmt_)
		}
	}
	return nil
}

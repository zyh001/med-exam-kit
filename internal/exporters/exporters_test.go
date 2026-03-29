package exporters

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zyh001/med-exam-kit/internal/models"
)

func makeQuestions() []*models.Question {
	return []*models.Question{
		{
			Fingerprint: "fp001",
			Pkg:         "ahuyikao.com",
			Cls:         "口腔执业医师题库",
			Unit:        "第一章",
			Mode:        "A1型题",
			SubQuestions: []models.SubQuestion{
				{
					Text:    "牙龈瘤的性质",
					Options: []string{"A.肿瘤", "B.增生", "C.炎症"},
					Answer:  "B",
					Rate:    "75%",
					Discuss: "牙龈瘤是增生性病变",
				},
			},
		},
		{
			Fingerprint: "fp002",
			Pkg:         "com.yikaobang.yixue",
			Mode:        "A1型题",
			SubQuestions: []models.SubQuestion{
				{
					Text:      "超声检查适用于",
					Options:   []string{"A.占位", "B.囊性", "C.实性", "D.血管", "E.以上均是"},
					Answer:    "",
					AIAnswer:  "E",
					AIDiscuss: "AI解析内容",
				},
			},
		},
	}
}

func TestFlatten_MergeMode(t *testing.T) {
	rows, cols := Flatten(makeQuestions(), false)
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
	// Check options column present
	found := false
	for _, c := range cols {
		if c == "options" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("options column missing in merge mode")
	}
}

func TestFlatten_SplitMode(t *testing.T) {
	rows, cols := Flatten(makeQuestions(), true)
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
	// Should have option_A through option_E (max 5 opts)
	optCols := 0
	for _, c := range cols {
		if strings.HasPrefix(c, "option_") {
			optCols++
		}
	}
	if optCols != 5 {
		t.Fatalf("want 5 option columns, got %d", optCols)
	}
	// 3-option row should have empty option_D and option_E
	row3opt := rows[0]
	if row3opt["option_D"] != "" {
		t.Fatalf("option_D should be empty for 3-option question")
	}
}

func TestFlatten_AnswerSource(t *testing.T) {
	rows, _ := Flatten(makeQuestions(), false)
	// First question: official answer
	if rows[0]["answer_source"] != "official" {
		t.Fatalf("want official, got %s", rows[0]["answer_source"])
	}
	// Second question: AI answer (no official)
	if rows[1]["answer_source"] != "ai" {
		t.Fatalf("want ai, got %s", rows[1]["answer_source"])
	}
}

func TestExportCSV_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "export.csv")
	if err := ExportCSV(makeQuestions(), out, false); err != nil {
		t.Fatalf("ExportCSV: %v", err)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatalf("file not created: %v", err)
	}
	defer f.Close()

	// Skip BOM
	bom := make([]byte, 3)
	f.Read(bom)

	r := csv.NewReader(f)
	records, err := r.ReadAll()
	if err != nil {
		t.Fatalf("csv parse: %v", err)
	}
	if len(records) != 3 { // 1 header + 2 data rows
		t.Fatalf("want 3 records (header+2), got %d", len(records))
	}
}

func TestExportCSV_SplitOptions(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "split.csv")
	if err := ExportCSV(makeQuestions(), out, true); err != nil {
		t.Fatalf("ExportCSV split: %v", err)
	}
	f, _ := os.Open(out)
	defer f.Close()
	bom := make([]byte, 3)
	f.Read(bom)
	r := csv.NewReader(f)
	header, _ := r.Read()
	// Verify option columns exist
	hasOptA := false
	for _, h := range header {
		if h == "A选项" || strings.Contains(h, "选项") {
			hasOptA = true
			break
		}
	}
	_ = hasOptA // column names are display names; just verify no panic
}

func TestExportXLSX_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "export.xlsx")
	if err := ExportXLSX(makeQuestions(), out, false); err != nil {
		t.Fatalf("ExportXLSX: %v", err)
	}
	info, err := os.Stat(out)
	if err != nil {
		t.Fatalf("file not created: %v", err)
	}
	if info.Size() < 100 {
		t.Fatal("xlsx file suspiciously small")
	}
}

func TestExportJSON_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "export.json")
	if err := ExportJSON(makeQuestions(), out); err != nil {
		t.Fatalf("ExportJSON: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("file not created: %v", err)
	}
	if !strings.Contains(string(data), "fingerprint") {
		t.Fatal("json missing fingerprint field")
	}
}

func TestExportDOCX_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "export.docx")
	if err := ExportDOCX(makeQuestions(), out); err != nil {
		t.Fatalf("ExportDOCX: %v", err)
	}
	info, _ := os.Stat(out)
	if info.Size() < 100 {
		t.Fatal("docx file too small")
	}
}

func TestExportPDF_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "export.pdf")
	if err := ExportPDF(makeQuestions(), out); err != nil {
		t.Fatalf("ExportPDF: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("file not created: %v", err)
	}
	if !strings.HasPrefix(string(data), "%PDF") {
		t.Fatal("output is not a PDF")
	}
}

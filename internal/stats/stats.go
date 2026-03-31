// Package stats provides question-bank statistics and terminal-formatted summaries.
package stats

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/zyh001/med-exam-kit/internal/models"
)

// DifficultyLabels maps internal keys to display labels.
var DifficultyLabels = map[string]string{
	"easy":    "简单 (≥80%)",
	"medium":  "中等 (60-80%)",
	"hard":    "较难 (40-60%)",
	"extreme": "困难 (<40%)",
	"unknown": "未知 (无正确率)",
}

var difficultyOrder = []string{"easy", "medium", "hard", "extreme", "unknown"}

// Summary holds the computed statistics.
type Summary struct {
	Total        int
	TotalSubQ    int
	ByMode       []KV
	ByUnit       []KV
	ByPkg        []KV
	ByCls        []KV
	ByDifficulty []KV
	UnitTotal    int
	LowRateCount int
	LowRateTop10 []LowRateItem
	Full         bool
}

type KV struct {
	Key   string
	Count int
}

// LowRateItem represents a question with a low correct rate.
type LowRateItem struct {
	Text   string
	Rate   string
	Answer string
	Unit   string
	Mode   string
}

func parseRate(raw string) (float64, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, false
	}
	s = strings.TrimSuffix(s, "%")
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 || v > 100 {
		return 0, false
	}
	return v, true
}

func classifyDifficulty(q *models.Question) string {
	var rates []float64
	for _, sq := range q.SubQuestions {
		if r, ok := parseRate(sq.Rate); ok {
			rates = append(rates, r)
		}
	}
	if len(rates) == 0 {
		return "unknown"
	}
	sum := 0.0
	for _, r := range rates {
		sum += r
	}
	avg := sum / float64(len(rates))
	switch {
	case avg >= 80:
		return "easy"
	case avg >= 60:
		return "medium"
	case avg >= 40:
		return "hard"
	default:
		return "extreme"
	}
}

// Summarize computes statistics for a list of questions.
func Summarize(questions []*models.Question, full bool) Summary {
	byMode := map[string]int{}
	byUnit := map[string]int{}
	byPkg := map[string]int{}
	byCls := map[string]int{}
	byDiff := map[string]int{}
	var lowRate []LowRateItem
	totalSQ := 0

	for _, q := range questions {
		byMode[q.Mode]++
		byUnit[q.Unit]++
		byPkg[q.Pkg]++
		byCls[q.Cls]++
		byDiff[classifyDifficulty(q)]++
		totalSQ += len(q.SubQuestions)

		for _, sq := range q.SubQuestions {
			if r, ok := parseRate(sq.Rate); ok && r < 50 {
				text := sq.Text
				if len([]rune(text)) > 60 {
					text = string([]rune(text)[:60])
				}
				lowRate = append(lowRate, LowRateItem{
					Text: text, Rate: sq.Rate, Answer: sq.Answer,
					Unit: q.Unit, Mode: q.Mode,
				})
			}
		}
	}

	// Sort low rate by rate ascending
	sort.Slice(lowRate, func(i, j int) bool {
		ri, _ := parseRate(lowRate[i].Rate)
		rj, _ := parseRate(lowRate[j].Rate)
		return ri < rj
	})
	top10 := lowRate
	if len(top10) > 10 {
		top10 = top10[:10]
	}

	// Build difficulty in order
	var diffKV []KV
	for _, k := range difficultyOrder {
		if v := byDiff[k]; v > 0 {
			diffKV = append(diffKV, KV{k, v})
		}
	}

	unitLimit := -1
	if !full {
		unitLimit = 20
	}

	return Summary{
		Total:        len(questions),
		TotalSubQ:    totalSQ,
		ByMode:       sortedKV(byMode, -1),
		ByUnit:       sortedKV(byUnit, unitLimit),
		ByPkg:        sortedKV(byPkg, -1),
		ByCls:        sortedKV(byCls, -1),
		ByDifficulty: diffKV,
		UnitTotal:    len(byUnit),
		LowRateCount: len(lowRate),
		LowRateTop10: top10,
		Full:         full,
	}
}

func sortedKV(m map[string]int, limit int) []KV {
	kvs := make([]KV, 0, len(m))
	for k, v := range m {
		kvs = append(kvs, KV{k, v})
	}
	sort.Slice(kvs, func(i, j int) bool {
		return kvs[i].Count > kvs[j].Count
	})
	if limit > 0 && len(kvs) > limit {
		kvs = kvs[:limit]
	}
	return kvs
}

// PrintSummary prints a formatted summary to stdout.
func PrintSummary(questions []*models.Question, full bool) {
	s := Summarize(questions, full)
	total := s.Total
	if total == 0 {
		total = 1
	}

	fmt.Printf("\n%s\n", strings.Repeat("=", 50))
	fmt.Println("📊 题目统计")
	fmt.Println(strings.Repeat("=", 50))
	fmt.Printf("总题数: %d 道大题, %d 道小题\n", s.Total, s.TotalSubQ)

	printSection := func(title string, data []KV, showBar, showPct bool) {
		fmt.Printf("\n%s:\n", title)
		if len(data) == 0 {
			fmt.Println("  (无数据)")
			return
		}
		colWidth := 4
		for _, item := range data {
			label := item.Key
			if strings.TrimSpace(label) == "" {
				label = "未知"
			}
			w := displayWidth(label)
			if w+2 > colWidth {
				colWidth = w + 2
			}
		}
		maxCount := 0
		for _, item := range data {
			if item.Count > maxCount {
				maxCount = item.Count
			}
		}
		for _, item := range data {
			label := item.Key
			if strings.TrimSpace(label) == "" {
				label = "未知"
			}
			padded := padRight(label, colWidth)
			pct := ""
			if showPct {
				pct = fmt.Sprintf("(%5.1f%%)", float64(item.Count)/float64(total)*100)
			}
			bar := ""
			if showBar && maxCount > 0 {
				barLen := int(math.Round(float64(item.Count) / float64(maxCount) * 20))
				bar = " " + strings.Repeat("■", barLen)
			}
			fmt.Printf("  %s %5d %s%s\n", padded, item.Count, pct, bar)
		}
	}

	printSection("按题型", s.ByMode, true, true)

	// Convert difficulty keys to labels
	diffLabeled := make([]KV, len(s.ByDifficulty))
	for i, d := range s.ByDifficulty {
		label := DifficultyLabels[d.Key]
		if label == "" {
			label = d.Key
		}
		diffLabeled[i] = KV{label, d.Count}
	}
	printSection("按难度", diffLabeled, true, true)
	printSection("按来源", s.ByPkg, true, true)
	printSection("按题库", s.ByCls, false, false)

	if full {
		printSection(fmt.Sprintf("按章节 (共 %d 个)", s.UnitTotal), s.ByUnit, false, false)
	} else {
		top10 := s.ByUnit
		if len(top10) > 10 {
			top10 = top10[:10]
		}
		printSection(fmt.Sprintf("按章节 (Top 10 / 共 %d 个)", s.UnitTotal), top10, false, false)
		if s.UnitTotal > 10 {
			fmt.Printf("  ... 还有 %d 个章节\n", s.UnitTotal-10)
		}
	}

	if s.LowRateCount > 0 {
		fmt.Printf("\n⚠️  正确率 < 50%% 的题目: %d 道\n", s.LowRateCount)
		for _, item := range s.LowRateTop10 {
			fmt.Printf("  [%s] %s...\n", item.Rate, item.Text)
		}
	}

	fmt.Println(strings.Repeat("=", 50))
}

// displayWidth calculates the terminal display width of a string.
func displayWidth(s string) int {
	w := 0
	for _, r := range s {
		if isWide(r) {
			w += 2
		} else {
			w++
		}
	}
	return w
}

func isWide(r rune) bool {
	if !utf8.ValidRune(r) {
		return false
	}
	// CJK characters and fullwidth forms
	if r >= 0x1100 && r <= 0x115F {
		return true
	}
	if r >= 0x2E80 && r <= 0x303E {
		return true
	}
	if r >= 0x3040 && r <= 0x33BF {
		return true
	}
	if r >= 0x3400 && r <= 0x4DBF {
		return true
	}
	if r >= 0x4E00 && r <= 0xA4CF {
		return true
	}
	if r >= 0xA960 && r <= 0xA97C {
		return true
	}
	if r >= 0xAC00 && r <= 0xD7FB {
		return true
	}
	if r >= 0xF900 && r <= 0xFAFF {
		return true
	}
	if r >= 0xFE30 && r <= 0xFE6F {
		return true
	}
	if r >= 0xFF01 && r <= 0xFF60 {
		return true
	}
	if r >= 0xFFE0 && r <= 0xFFE6 {
		return true
	}
	if r >= 0x20000 && r <= 0x2FFFF {
		return true
	}
	if r >= 0x30000 && r <= 0x3FFFF {
		return true
	}
	return false
}

func padRight(s string, width int) string {
	pad := width - displayWidth(s)
	if pad <= 0 {
		return s
	}
	return s + strings.Repeat(" ", pad)
}

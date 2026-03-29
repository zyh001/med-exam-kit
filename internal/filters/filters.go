package filters

import (
	"strconv"
	"strings"

	"github.com/med-exam-kit/med-exam-kit/internal/models"
)

// FilterCriteria holds all filter conditions (AND semantics).
type FilterCriteria struct {
	Modes   []string // mode whitelist (substring match)
	Units   []string // unit keyword whitelist
	Pkgs    []string // pkg whitelist (substring match)
	ClsList []string // cls whitelist
	Keyword string   // stem/text keyword search
	MinRate int      // 0–100
	MaxRate int      // 0–100
}

// NewCriteria returns a FilterCriteria with sensible defaults (no filtering).
func NewCriteria() FilterCriteria {
	return FilterCriteria{MaxRate: 100}
}

// Apply filters the question list according to c and returns the survivors.
func Apply(questions []*models.Question, c FilterCriteria) []*models.Question {
	result := make([]*models.Question, 0, len(questions))

	for _, q := range questions {
		if len(c.Modes) > 0 && !anyContains(q.Mode, c.Modes) {
			continue
		}
		if len(c.Pkgs) > 0 && !anyContains(q.Pkg, c.Pkgs) {
			continue
		}
		if len(c.ClsList) > 0 && !anyContains(q.Cls, c.ClsList) {
			continue
		}
		if len(c.Units) > 0 && !anyContains(q.Unit, c.Units) {
			continue
		}
		if c.Keyword != "" {
			kw := strings.ToLower(c.Keyword)
			found := strings.Contains(strings.ToLower(q.Stem), kw)
			for _, sq := range q.SubQuestions {
				if strings.Contains(strings.ToLower(sq.Text), kw) {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		if c.MinRate > 0 || c.MaxRate < 100 {
			rates := collectRates(q)
			if len(rates) > 0 {
				avg := average(rates)
				if avg < float64(c.MinRate) || avg > float64(c.MaxRate) {
					continue
				}
			}
		}
		result = append(result, q)
	}
	return result
}

// ── helpers ────────────────────────────────────────────────────────────

func anyContains(s string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}

func collectRates(q *models.Question) []float64 {
	var rates []float64
	for _, sq := range q.SubQuestions {
		if sq.Rate == "" {
			continue
		}
		clean := strings.TrimSuffix(strings.TrimSpace(sq.Rate), "%")
		if v, err := strconv.ParseFloat(clean, 64); err == nil {
			rates = append(rates, v)
		}
	}
	return rates
}

func average(vs []float64) float64 {
	if len(vs) == 0 {
		return 0
	}
	var sum float64
	for _, v := range vs {
		sum += v
	}
	return sum / float64(len(vs))
}

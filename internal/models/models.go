package models

import (
	"regexp"
	"strings"
	"unicode"
)

// validAnswerRe matches a legitimate answer string: 1–5 uppercase A-E letters (single or multi).
// Examples: "A", "B", "CE", "ABD"
var validAnswerRe = regexp.MustCompile(`^[A-E]{1,5}$`)

// isLikelyAnswer returns true if s looks like a multiple-choice answer (A/B/C/D/E combination).
func isLikelyAnswer(s string) bool {
	s = strings.TrimSpace(s)
	return validAnswerRe.MatchString(s)
}

// isLikelyDiscuss returns true if s looks like an explanation text (contains CJK/punctuation/spaces).
func isLikelyDiscuss(s string) bool {
	s = strings.TrimSpace(s)
	if len([]rune(s)) < 4 {
		return false
	}
	for _, r := range s {
		if unicode.Is(unicode.Han, r) || unicode.IsPunct(r) || unicode.IsSpace(r) {
			return true
		}
	}
	return false
}

// SubQuestion is a single question item (A1/A2 are also stored as one SubQuestion).
type SubQuestion struct {
	Text         string   `json:"text"`
	Options      []string `json:"options"`
	Answer       string   `json:"answer"`
	Rate         string   `json:"rate"`
	ErrorProne   string   `json:"error_prone"`
	Discuss      string   `json:"discuss"`
	Point        string   `json:"point"`
	AIAnswer     string   `json:"ai_answer"`
	AIDiscuss    string   `json:"ai_discuss"`
	AIConfidence float64  `json:"ai_confidence"`
	AIModel      string   `json:"ai_model"`
	AIStatus     string   `json:"ai_status"` // pending / accepted / rejected
}

// EffAnswer returns the effective answer: official first, AI fallback.
func (sq *SubQuestion) EffAnswer() string {
	if a := strings.TrimSpace(sq.Answer); a != "" {
		return a
	}
	return strings.TrimSpace(sq.AIAnswer)
}

// EffDiscuss returns the effective explanation: official first, AI fallback.
func (sq *SubQuestion) EffDiscuss() string {
	if d := strings.TrimSpace(sq.Discuss); d != "" {
		return d
	}
	return strings.TrimSpace(sq.AIDiscuss)
}

// AnswerSource returns "official", "ai", or "".
// When ai_answer matches eff_answer → "ai" (includes: official empty, or official == AI).
// Otherwise official has value → "official".
// Matches Python behaviour exactly.
func (sq *SubQuestion) AnswerSource() string {
	ai := strings.TrimSpace(sq.AIAnswer)
	eff := sq.EffAnswer()
	if ai != "" && ai == eff {
		return "ai"
	}
	if eff != "" {
		return "official"
	}
	return ""
}

// DiscussSource returns "official", "ai", or "".
// Same logic as AnswerSource but for the discuss field.
func (sq *SubQuestion) DiscussSource() string {
	ai := strings.TrimSpace(sq.AIDiscuss)
	eff := sq.EffDiscuss()
	if ai != "" && ai == eff {
		return "ai"
	}
	if eff != "" {
		return "official"
	}
	return ""
}

// IsAnswerDiscussSwapped reports whether answer and discuss appear to have been
// accidentally swapped: answer looks like an explanation and discuss looks like
// a valid option letter.
func (sq *SubQuestion) IsAnswerDiscussSwapped() bool {
	ans := strings.TrimSpace(sq.Answer)
	dis := strings.TrimSpace(sq.Discuss)
	if ans == "" || dis == "" {
		return false
	}
	// Swapped when: answer is long explanation-like text AND discuss is a valid option
	return isLikelyDiscuss(ans) && isLikelyAnswer(dis)
}

// SanitizeAnswerDiscuss fixes a swapped answer/discuss pair in-place.
// Returns true if a swap was detected and corrected.
func (sq *SubQuestion) SanitizeAnswerDiscuss() bool {
	if !sq.IsAnswerDiscussSwapped() {
		return false
	}
	sq.Answer, sq.Discuss = sq.Discuss, sq.Answer
	return true
}

// Question is the unified question model all parsers normalise to.
type Question struct {
	Fingerprint   string         `json:"fingerprint"`
	Name          string         `json:"name"`
	Pkg           string         `json:"pkg"`
	Cls           string         `json:"cls"`
	Unit          string         `json:"unit"`
	Mode          string         `json:"mode"`
	Stem          string         `json:"stem"`           // A3/A4 shared stem
	SharedOptions []string       `json:"shared_options"` // B-type shared options
	SubQuestions  []SubQuestion  `json:"sub_questions"`
	Discuss       string         `json:"discuss"`
	SourceFile    string         `json:"source_file"`
	Raw           map[string]any `json:"raw,omitempty"` // original JSON, not persisted to .mqb
}

// SanitizeQuestions fixes answer/discuss swaps in-place across all questions.
// It returns counts: fixed = number of sub-questions corrected.
// Call this once after loading a bank to silently repair data quality issues.
func SanitizeQuestions(questions []*Question) (fixed int) {
	for _, q := range questions {
		for i := range q.SubQuestions {
			if q.SubQuestions[i].SanitizeAnswerDiscuss() {
				fixed++
			}
		}
	}
	return fixed
}

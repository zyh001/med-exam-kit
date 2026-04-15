package models

import (
	"regexp"
	"strings"
	"unicode"
)

// validAnswerRe matches a legitimate answer string: 1–10 uppercase A-J letters.
// Covers questions with up to 10 options (A through J).
// Examples: "A", "B", "CE", "ABD", "FGJ"
var validAnswerRe = regexp.MustCompile(`^[A-J]{1,10}$`)

// isLikelyAnswer returns true if s looks like a multiple-choice answer
// (combination of option letters, default A-J range).
func isLikelyAnswer(s string) bool {
	s = strings.TrimSpace(s)
	return validAnswerRe.MatchString(s)
}

// isLikelyAnswerForOptions returns true if s looks like a valid answer
// given the question's actual option count. More precise than isLikelyAnswer.
// maxOpt is the number of options (e.g. 5 → letters A-E, 10 → letters A-J).
func isLikelyAnswerForOptions(s string, maxOpt int) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	if maxOpt <= 0 {
		maxOpt = 10 // default: allow up to J
	}
	maxLetter := rune('A') + rune(maxOpt) - 1 // e.g. maxOpt=5 → 'E', maxOpt=10 → 'J'
	for _, r := range s {
		if r < 'A' || r > maxLetter {
			return false
		}
	}
	// All chars are valid option letters; must also be all unique (no "AABB")
	seen := make(map[rune]bool)
	for _, r := range s {
		if seen[r] {
			return false // duplicate letter → not a valid answer
		}
		seen[r] = true
	}
	return true
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
// It uses the question's actual options count for precise letter-range checking.
func (sq *SubQuestion) IsAnswerDiscussSwapped() bool {
	ans := strings.TrimSpace(sq.Answer)
	dis := strings.TrimSpace(sq.Discuss)
	if ans == "" || dis == "" {
		return false
	}
	// Use actual option count for more accurate detection (supports up to 10 options)
	maxOpt := len(sq.Options)
	if maxOpt == 0 {
		maxOpt = 10 // no options info → be permissive, allow A-J
	}
	// Swapped when: answer is long explanation-like text AND discuss is a valid option
	return isLikelyDiscuss(ans) && isLikelyAnswerForOptions(dis, maxOpt)
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

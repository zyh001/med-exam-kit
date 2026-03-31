package models

import "strings"

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

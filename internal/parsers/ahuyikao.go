package parsers

import (
	"github.com/zyh001/med-exam-kit/internal/models"
)

// AhuyikaoParser handles JSON from com.ahuxueshu / ahuyikao.com.
type AhuyikaoParser struct{}

func (p *AhuyikaoParser) CanHandle(raw map[string]any) bool {
	return str(raw["pkg"]) == "com.ahuxueshu"
}

func (p *AhuyikaoParser) Parse(raw map[string]any) (*models.Question, error) {
	mode := str(raw["mode"])
	q := &models.Question{
		Name: str(raw["name"]),
		Pkg:  str(raw["pkg"]),
		Cls:  str(raw["cls"]),
		Unit: str(raw["unit"]),
		Mode: mode,
		Raw:  raw,
	}

	switch {
	case contains(mode, "B") && contains(mode, "型题"):
		q.SharedOptions = strSlice(raw["shared_options"])
		q.Discuss = str(raw["discuss"])
		for _, item := range anySlice(raw["sub_questions"]) {
			sq := item.(map[string]any)
			q.SubQuestions = append(q.SubQuestions, models.SubQuestion{
				Text:       str(sq["test"]),
				Options:    strSliceCopy(q.SharedOptions), // independent copy
				Answer:     str(sq["answer"]),
				Rate:       str(sq["rate"]),
				ErrorProne: str(sq["error_prone"]),
				Discuss:    str(sq["discuss"]),
			})
		}

	case contains(mode, "A3") || contains(mode, "A4"):
		q.Stem = str(raw["stem"])
		for _, item := range anySlice(raw["sub_questions"]) {
			sq := item.(map[string]any)
			q.SubQuestions = append(q.SubQuestions, models.SubQuestion{
				Text:       str(sq["test"]),
				Options:    strSlice(sq["option"]),
				Answer:     str(sq["answer"]),
				Rate:       str(sq["rate"]),
				ErrorProne: str(sq["error_prone"]),
				Discuss:    str(sq["discuss"]),
			})
		}

	default: // A1 / A2
		q.SubQuestions = append(q.SubQuestions, models.SubQuestion{
			Text:       str(raw["test"]),
			Options:    strSlice(raw["option"]),
			Answer:     str(raw["answer"]),
			Rate:       str(raw["rate"]),
			ErrorProne: str(raw["error_prone"]),
			Discuss:    str(raw["discuss"]),
		})
	}
	return q, nil
}

// ── shared helpers (used by both parsers) ─────────────────────────────

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func strSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func strSliceCopy(src []string) []string {
	c := make([]string, len(src))
	copy(c, src)
	return c
}

func anySlice(v any) []any {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	return arr
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) &&
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}()
}

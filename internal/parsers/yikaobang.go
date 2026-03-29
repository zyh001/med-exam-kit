package parsers

import (
	"github.com/zyh001/med-exam-kit/internal/models"
)

// YikaobangParser handles JSON from com.yikaobang.yixue.
type YikaobangParser struct{}

func (p *YikaobangParser) CanHandle(raw map[string]any) bool {
	return contains(str(raw["pkg"]), "yikaobang")
}

func (p *YikaobangParser) Parse(raw map[string]any) (*models.Question, error) {
	mode := str(raw["mode"])
	q := &models.Question{
		Name: str(raw["name"]),
		Pkg:  str(raw["pkg"]),
		Cls:  str(raw["cls"]),
		Unit: str(raw["unit"]),
		Mode: mode,
		Raw:  raw,
	}

	hasSub := len(anySlice(raw["sub_questions"])) > 0

	switch {
	case contains(mode, "B") && contains(mode, "型题") && hasSub:
		// Multi-question B-type with shared options
		q.SharedOptions = strSlice(raw["shared_options"])
		q.Discuss = str(raw["discuss"])
		for _, item := range anySlice(raw["sub_questions"]) {
			sq := item.(map[string]any)
			q.SubQuestions = append(q.SubQuestions, models.SubQuestion{
				Text:       str(sq["test"]),
				Options:    strSliceCopy(q.SharedOptions),
				Answer:     str(sq["answer"]),
				Rate:       str(sq["rate"]),
				ErrorProne: str(sq["error_prone"]),
				Discuss:    str(sq["discuss"]),
				Point:      str(sq["point"]),
			})
		}

	case (contains(mode, "A3") || contains(mode, "A4") || contains(mode, "案例")) && hasSub:
		// Standard multi-subquestion case
		q.Stem = str(raw["test"])
		for _, item := range anySlice(raw["sub_questions"]) {
			sq := item.(map[string]any)
			q.SubQuestions = append(q.SubQuestions, models.SubQuestion{
				Text:       str(sq["sub_test"]),
				Options:    strSlice(sq["option"]),
				Answer:     str(sq["answer"]),
				Rate:       str(sq["rate"]),
				ErrorProne: str(sq["error_prone"]),
				Discuss:    str(sq["discuss"]),
				Point:      str(sq["point"]),
			})
		}

	case contains(mode, "A3") || contains(mode, "A4") || contains(mode, "案例"):
		// Single-question fallback for A3/A4 label
		q.Stem = str(raw["test"])
		q.SubQuestions = append(q.SubQuestions, models.SubQuestion{
			Text:       "",
			Options:    strSlice(raw["option"]),
			Answer:     str(raw["answer"]),
			Rate:       str(raw["rate"]),
			ErrorProne: str(raw["error_prone"]),
			Discuss:    str(raw["discuss"]),
			Point:      str(raw["point"]),
		})

	default: // A1 / A2 / single B-type
		q.SubQuestions = append(q.SubQuestions, models.SubQuestion{
			Text:       str(raw["test"]),
			Options:    strSlice(raw["option"]),
			Answer:     str(raw["answer"]),
			Rate:       str(raw["rate"]),
			ErrorProne: str(raw["error_prone"]),
			Discuss:    str(raw["discuss"]),
			Point:      str(raw["point"]),
		})
	}
	return q, nil
}

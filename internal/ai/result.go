package ai

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/zyh001/med-exam-kit/internal/models"
)

// ParseResponse extracts a JSON object from AI output, handling markdown fences etc.
func ParseResponse(raw string) map[string]any {
	text := strings.TrimSpace(raw)
	if text == "" {
		return nil
	}

	// Strategy 1: direct JSON
	var m map[string]any
	if err := json.Unmarshal([]byte(text), &m); err == nil {
		return m
	}

	// Strategy 2: markdown code block
	reFence := regexp.MustCompile("(?s)```(?:json)?\\s*(\\{.*?\\})\\s*```")
	if matches := reFence.FindStringSubmatch(text); len(matches) > 1 {
		if err := json.Unmarshal([]byte(matches[1]), &m); err == nil {
			return m
		}
	}

	// Strategy 3: first JSON object
	reObj := regexp.MustCompile(`(?s)\{.*\}`)
	if match := reObj.FindString(text); match != "" {
		if err := json.Unmarshal([]byte(match), &m); err == nil {
			return m
		}
	}

	return nil
}

// ValidateResult checks whether the result has the required fields.
// Returns (ok, missingFields).
func ValidateResult(result map[string]any, needAnswer, needDiscuss bool) (bool, []string) {
	var missing []string
	if needAnswer {
		a, _ := result["answer"].(string)
		if strings.TrimSpace(a) == "" {
			missing = append(missing, "answer")
		}
	}
	if needDiscuss {
		d, _ := result["discuss"].(string)
		if strings.TrimSpace(d) == "" {
			missing = append(missing, "discuss")
		}
	}
	return len(missing) == 0, missing
}

// ApplyToSubQuestion writes AI result into the SubQuestion fields.
// If overwrite is true, also fills empty official answer/discuss from AI.
func ApplyToSubQuestion(sq *models.SubQuestion, result map[string]any, modelName string, overwrite bool) {
	if result == nil {
		return
	}

	aiAnswer := strings.TrimSpace(strAny(result["answer"]))
	aiDiscuss := strings.TrimSpace(strAny(result["discuss"]))

	var aiConfidence float64
	switch v := result["confidence"].(type) {
	case float64:
		aiConfidence = v
	case string:
		aiConfidence, _ = strconv.ParseFloat(v, 64)
	}

	if aiAnswer != "" {
		sq.AIAnswer = aiAnswer
	}
	if aiDiscuss != "" {
		sq.AIDiscuss = aiDiscuss
	}
	sq.AIConfidence = aiConfidence
	sq.AIModel = modelName
	sq.AIStatus = "pending"

	if overwrite {
		if aiAnswer != "" && strings.TrimSpace(sq.Answer) == "" {
			sq.Answer = aiAnswer
		}
		if aiDiscuss != "" && strings.TrimSpace(sq.Discuss) == "" {
			sq.Discuss = aiDiscuss
		}
	}
}

func strAny(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

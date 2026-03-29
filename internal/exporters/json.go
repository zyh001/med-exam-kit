package exporters

import (
	"encoding/json"
	"os"

	"github.com/zyh001/med-exam-kit/internal/models"
)

type exportSubQ struct {
	Text          string   `json:"text"`
	Options       []string `json:"options"`
	Answer        string   `json:"answer"`
	AnswerSource  string   `json:"answer_source"`
	Discuss       string   `json:"discuss"`
	DiscussSource string   `json:"discuss_source"`
	Rate          string   `json:"rate"`
	ErrorProne    string   `json:"error_prone"`
	Point         string   `json:"point"`
	AIAnswer      string   `json:"ai_answer,omitempty"`
	AIDiscuss     string   `json:"ai_discuss,omitempty"`
	AIConfidence  float64  `json:"ai_confidence,omitempty"`
	AIModel       string   `json:"ai_model,omitempty"`
	AIStatus      string   `json:"ai_status,omitempty"`
}

type exportQuestion struct {
	Fingerprint   string       `json:"fingerprint"`
	Name          string       `json:"name"`
	Pkg           string       `json:"pkg"`
	Cls           string       `json:"cls"`
	Unit          string       `json:"unit"`
	Mode          string       `json:"mode"`
	Stem          string       `json:"stem,omitempty"`
	SharedOptions []string     `json:"shared_options,omitempty"`
	SubQuestions  []exportSubQ `json:"sub_questions"`
	Discuss       string       `json:"discuss,omitempty"`
}

// ExportJSON writes questions to a JSON file.
func ExportJSON(questions []*models.Question, outPath string) error {
	records := make([]exportQuestion, len(questions))
	for i, q := range questions {
		sqs := make([]exportSubQ, len(q.SubQuestions))
		for j, sq := range q.SubQuestions {
			sqs[j] = exportSubQ{
				Text:          sq.Text,
				Options:       sq.Options,
				Answer:        sq.EffAnswer(),
				AnswerSource:  sq.AnswerSource(),
				Discuss:       sq.EffDiscuss(),
				DiscussSource: sq.DiscussSource(),
				Rate:          sq.Rate,
				ErrorProne:    sq.ErrorProne,
				Point:         sq.Point,
				AIAnswer:      sq.AIAnswer,
				AIDiscuss:     sq.AIDiscuss,
				AIConfidence:  sq.AIConfidence,
				AIModel:       sq.AIModel,
				AIStatus:      sq.AIStatus,
			}
		}
		records[i] = exportQuestion{
			Fingerprint:   q.Fingerprint,
			Name:          q.Name,
			Pkg:           q.Pkg,
			Cls:           q.Cls,
			Unit:          q.Unit,
			Mode:          q.Mode,
			Stem:          q.Stem,
			SharedOptions: q.SharedOptions,
			SubQuestions:  sqs,
			Discuss:       q.Discuss,
		}
	}

	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(records)
}

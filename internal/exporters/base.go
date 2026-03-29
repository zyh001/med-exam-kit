package exporters

import (
	"fmt"

	"github.com/zyh001/med-exam-kit/internal/models"
)

// Columns defines the ordered export columns.
var Columns = []string{
	"fingerprint", "name", "pkg", "cls", "unit", "mode",
	"stem", "text",
	"answer", "answer_source",
	"discuss", "discuss_source",
	"rate", "error_prone", "point",
	"ai_answer", "ai_discuss", "ai_confidence", "ai_model", "ai_status",
	"options", // joined: "A.xxx|B.yyy|..."
}

// Row is a flat map representing one sub-question.
type Row map[string]string

// SplitColumns returns columns for split-option mode (one column per option letter).
func SplitColumns(maxOpts int) []string {
	base := []string{
		"fingerprint", "name", "pkg", "cls", "unit", "mode", "stem", "text",
		"answer", "answer_source", "discuss", "discuss_source",
		"rate", "error_prone", "point",
		"ai_answer", "ai_discuss", "ai_confidence", "ai_model", "ai_status",
	}
	for i := 0; i < maxOpts; i++ {
		base = append(base, fmt.Sprintf("option_%c", 'A'+i))
	}
	return base
}

// Flatten converts questions to export rows.
// If splitOptions is true, each option gets its own column.
func Flatten(questions []*models.Question, splitOptions bool) ([]Row, []string) {
	// Determine max option count for split mode
	maxOpts := 0
	if splitOptions {
		for _, q := range questions {
			for _, sq := range q.SubQuestions {
				if len(sq.Options) > maxOpts {
					maxOpts = len(sq.Options)
				}
			}
		}
	}

	cols := Columns
	if splitOptions {
		cols = SplitColumns(maxOpts)
	}

	var rows []Row
	for _, q := range questions {
		for _, sq := range q.SubQuestions {
			r := Row{
				"fingerprint":    q.Fingerprint,
				"name":           q.Name,
				"pkg":            q.Pkg,
				"cls":            q.Cls,
				"unit":           q.Unit,
				"mode":           q.Mode,
				"stem":           q.Stem,
				"text":           sq.Text,
				"answer":         sq.EffAnswer(),
				"answer_source":  sq.AnswerSource(),
				"discuss":        sq.EffDiscuss(),
				"discuss_source": sq.DiscussSource(),
				"rate":           sq.Rate,
				"error_prone":    sq.ErrorProne,
				"point":          sq.Point,
				"ai_answer":      sq.AIAnswer,
				"ai_discuss":     sq.AIDiscuss,
				"ai_confidence":  fmt.Sprintf("%.2f", sq.AIConfidence),
				"ai_model":       sq.AIModel,
				"ai_status":      sq.AIStatus,
			}
			if splitOptions {
				for i := 0; i < maxOpts; i++ {
					key := fmt.Sprintf("option_%c", 'A'+i)
					if i < len(sq.Options) {
						r[key] = sq.Options[i]
					} else {
						r[key] = ""
					}
				}
			} else {
				opts := ""
				for i, o := range sq.Options {
					if i > 0 {
						opts += "|"
					}
					opts += o
				}
				r["options"] = opts
			}
			rows = append(rows, r)
		}
	}
	return rows, cols
}

// ColumnDisplayName maps internal column names to Chinese display names.
var ColumnDisplayName = map[string]string{
	"fingerprint":    "指纹",
	"name":           "文件名",
	"pkg":            "来源App",
	"cls":            "题库",
	"unit":           "章节",
	"mode":           "题型",
	"stem":           "共享题干",
	"text":           "题目",
	"answer":         "答案",
	"answer_source":  "答案来源",
	"discuss":        "解析",
	"discuss_source": "解析来源",
	"rate":           "正确率",
	"error_prone":    "易错项",
	"point":          "考点",
	"ai_answer":      "AI答案",
	"ai_discuss":     "AI解析",
	"ai_confidence":  "AI置信度",
	"ai_model":       "AI模型",
	"ai_status":      "AI状态",
	"options":        "选项",
}

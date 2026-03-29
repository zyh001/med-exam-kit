package parsers

import (
	"fmt"

	"github.com/med-exam-kit/med-exam-kit/internal/models"
)

// Parser converts a raw JSON map (one file) into a Question.
type Parser interface {
	CanHandle(raw map[string]any) bool
	Parse(raw map[string]any) (*models.Question, error)
}

var registry = map[string]Parser{}

// Register adds a named parser to the global registry.
func Register(name string, p Parser) {
	registry[name] = p
}

// Get returns a registered parser by name.
func Get(name string) (Parser, error) {
	p, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("parsers: unknown parser %q", name)
	}
	return p, nil
}

// DefaultParserMap maps pkg identifiers to parser names.
var DefaultParserMap = map[string]string{
	"ahuyikao.com":        "ahuyikao",
	"com.ahuxueshu":       "ahuyikao",
	"com.yikaobang.yixue": "yikaobang",
}

func init() {
	Register("ahuyikao", &AhuyikaoParser{})
	Register("yikaobang", &YikaobangParser{})
}

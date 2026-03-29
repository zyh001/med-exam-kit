package loader

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zyh001/med-exam-kit/internal/models"
	"github.com/zyh001/med-exam-kit/internal/parsers"
)

// Load scans inputDir for *.json files, dispatches each to the correct parser
// and returns all successfully parsed questions.
//
// parserMap example: {"ahuyikao.com": "ahuyikao", "com.yikaobang.yixue": "yikaobang"}
func Load(inputDir string, parserMap map[string]string) ([]*models.Question, error) {
	entries, err := collectJSON(inputDir)
	if err != nil {
		return nil, err
	}

	var questions []*models.Question
	skipped := 0

	for _, path := range entries {
		data, err := os.ReadFile(path)
		if err != nil {
			skipped++
			continue
		}

		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err != nil {
			skipped++
			continue
		}

		pkg := ""
		if v, ok := raw["pkg"].(string); ok {
			pkg = v
		}

		parserName := resolveParser(pkg, parserMap)
		if parserName == "" {
			skipped++
			continue
		}

		p, err := parsers.Get(parserName)
		if err != nil {
			skipped++
			continue
		}

		q, err := p.Parse(raw)
		if err != nil {
			skipped++
			continue
		}

		abs, _ := filepath.Abs(path)
		q.SourceFile = abs
		questions = append(questions, q)
	}

	if skipped > 0 {
		fmt.Fprintf(os.Stderr, "loader: 跳过 %d 个无法解析的文件\n", skipped)
	}
	return questions, nil
}

func resolveParser(pkg string, parserMap map[string]string) string {
	if name, ok := parserMap[pkg]; ok {
		return name
	}
	// fallback: substring match
	for key, name := range parserMap {
		if strings.Contains(pkg, key) || strings.Contains(key, pkg) {
			return name
		}
	}
	return ""
}

func collectJSON(dir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable dirs
		}
		if !d.IsDir() && strings.ToLower(filepath.Ext(path)) == ".json" {
			files = append(files, path)
		}
		return nil
	})
	return files, err
}

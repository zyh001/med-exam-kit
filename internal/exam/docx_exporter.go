package exam

import (
	"archive/zip"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/zyh001/med-exam-kit/internal/models"
)

// ExportDOCX writes the exam paper to a .docx file.
func ExportDOCX(questions []*models.Question, cfg Config, outputPath string) (string, error) {
	dir := filepath.Dir(outputPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	fp := outputPath
	if filepath.Ext(fp) != ".docx" {
		fp += ".docx"
	}

	f, err := os.Create(fp)
	if err != nil {
		return "", err
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	defer zw.Close()

	docXML := buildExamDocument(questions, cfg)

	writeZip(zw, "[Content_Types].xml", examContentTypes())
	writeZip(zw, "_rels/.rels", examRels())
	writeZip(zw, "word/_rels/document.xml.rels", examWordRels())
	writeZip(zw, "word/styles.xml", examStyles())
	writeZip(zw, "word/document.xml", docXML)

	return fp, nil
}

func buildExamDocument(questions []*models.Question, cfg Config) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	b.WriteString(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">`)
	b.WriteString(`<w:body>`)

	// Title
	b.WriteString(examPara("title", cfg.Title, false, true))
	if cfg.Subtitle != "" {
		b.WriteString(examPara("subtitle", cfg.Subtitle, false, true))
	}

	// Info line
	var info []string
	if cfg.TimeLimit > 0 {
		info = append(info, fmt.Sprintf("考试时间: %d 分钟", cfg.TimeLimit))
	}
	if cfg.TotalScore > 0 {
		info = append(info, fmt.Sprintf("满分: %d 分", cfg.TotalScore))
	}
	totalSubs := 0
	for _, q := range questions {
		totalSubs += len(q.SubQuestions)
	}
	info = append(info, fmt.Sprintf("共 %d 题", totalSubs))
	if cfg.ScorePerSub > 0 {
		info = append(info, fmt.Sprintf("每题 %.1f 分", cfg.ScorePerSub))
	}
	b.WriteString(examPara("info", strings.Join(info, "    "), false, true))
	b.WriteString(examPara("separator", strings.Repeat("━", 43), false, false))

	// Questions
	currentMode := ""
	globalIdx := 0
	for _, q := range questions {
		if q.Mode != currentMode {
			currentMode = q.Mode
			b.WriteString(examPara("mode_heading", "【"+currentMode+"】", true, false))
		}

		if len(q.SubQuestions) > 1 || q.Stem != "" {
			// Compound question
			stem := q.Stem
			if stem != "" {
				b.WriteString(examPara("compound_hint",
					fmt.Sprintf("（%d～%d 题共用题干）", globalIdx+1, globalIdx+len(q.SubQuestions)),
					true, false))
				b.WriteString(examPara("stem", stem, false, false))
			}
			if len(q.SharedOptions) > 0 {
				for _, opt := range q.SharedOptions {
					b.WriteString(examPara("option", "    "+opt, false, false))
				}
			}
			for _, sq := range q.SubQuestions {
				globalIdx++
				b.WriteString(examPara("question", fmt.Sprintf("%d. %s", globalIdx, sq.Text), false, false))
				if len(q.SharedOptions) == 0 {
					for _, opt := range sq.Options {
						b.WriteString(examPara("option", "    "+opt, false, false))
					}
				}
				if cfg.ShowAnswers {
					b.WriteString(examPara("inline_answer", "【答案】"+sq.EffAnswer(), false, false))
				}
			}
		} else if len(q.SubQuestions) == 1 {
			globalIdx++
			sq := q.SubQuestions[0]
			b.WriteString(examPara("question", fmt.Sprintf("%d. %s", globalIdx, sq.Text), false, false))
			for _, opt := range sq.Options {
				b.WriteString(examPara("option", "    "+opt, false, false))
			}
			if cfg.ShowAnswers {
				b.WriteString(examPara("inline_answer", "【答案】"+sq.EffAnswer(), false, false))
			}
		}
		b.WriteString(examPara("spacer", "", false, false))
	}

	// Answer sheet
	if cfg.AnswerSheet {
		b.WriteString(`<w:p><w:r><w:br w:type="page"/></w:r></w:p>`)
		b.WriteString(examPara("title", "参考答案", false, true))
		b.WriteString(examPara("spacer", "", false, false))

		if cfg.ShowDiscuss {
			currentMode = ""
			idx := 0
			for _, q := range questions {
				if q.Mode != currentMode {
					currentMode = q.Mode
					b.WriteString(examPara("mode_heading", "【"+currentMode+"】", true, false))
				}
				for _, sq := range q.SubQuestions {
					idx++
					ans := sq.EffAnswer()
					if ans == "" {
						ans = "—"
					}
					src := ""
					if sq.AnswerSource() == "ai" {
						src = "(AI)"
					}
					line := fmt.Sprintf("%d. %s%s", idx, ans, src)
					dis := sq.EffDiscuss()
					if dis != "" {
						line += "  " + dis
					}
					isAI := sq.AnswerSource() == "ai"
					b.WriteString(examPara("answer_detail", line, false, false))
					_ = isAI
				}
			}
		}

		// Quick reference
		b.WriteString(examPara("mode_heading", "答案速查", true, false))
		idx := 0
		var parts []string
		for _, q := range questions {
			for _, sq := range q.SubQuestions {
				idx++
				ans := sq.EffAnswer()
				if ans == "" {
					ans = "—"
				}
				if sq.AnswerSource() == "ai" {
					ans += "*"
				}
				parts = append(parts, fmt.Sprintf("%d-%s", idx, ans))
			}
		}
		// Print in rows of 10
		for i := 0; i < len(parts); i += 10 {
			end := i + 10
			if end > len(parts) {
				end = len(parts)
			}
			line := strings.Join(parts[i:end], "  ")
			b.WriteString(examPara("quick_ref", line, false, false))
		}

		// AI legend
		hasAI := false
		for _, q := range questions {
			for _, sq := range q.SubQuestions {
				if sq.AnswerSource() == "ai" {
					hasAI = true
					break
				}
			}
			if hasAI {
				break
			}
		}
		if hasAI {
			b.WriteString(examPara("ai_note", "* 标注表示该答案由 AI 补全，建议人工核对", false, false))
		}
	}

	b.WriteString(`<w:sectPr/>`)
	b.WriteString(`</w:body></w:document>`)
	return b.String()
}

func examPara(style, text string, bold, center bool) string {
	var b strings.Builder
	b.WriteString(`<w:p>`)
	b.WriteString(`<w:pPr><w:pStyle w:val="` + style + `"/>`)
	if center {
		b.WriteString(`<w:jc w:val="center"/>`)
	}
	b.WriteString(`</w:pPr>`)
	if text != "" {
		b.WriteString(`<w:r>`)
		if bold {
			b.WriteString(`<w:rPr><w:b/></w:rPr>`)
		}
		b.WriteString(`<w:t xml:space="preserve">`)
		b.WriteString(examXMLEsc(text))
		b.WriteString(`</w:t></w:r>`)
	}
	b.WriteString(`</w:p>`)
	return b.String()
}

func examContentTypes() string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml"  ContentType="application/xml"/>
  <Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
  <Override PartName="/word/styles.xml"   ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.styles+xml"/>
</Types>`
}

func examRels() string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
</Relationships>`
}

func examWordRels() string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>
</Relationships>`
}

func examStyles() string {
	styles := []struct{ id, name, sz, color string }{
		{"title", "标题", "36", "000000"},
		{"subtitle", "副标题", "24", "333333"},
		{"info", "信息行", "20", "666666"},
		{"mode_heading", "题型标题", "24", "1F497D"},
		{"compound_hint", "共用题干提示", "20", "333333"},
		{"stem", "题干", "21", "333333"},
		{"question", "题目", "21", "000000"},
		{"option", "选项", "20", "000000"},
		{"inline_answer", "内嵌答案", "18", "008000"},
		{"answer_detail", "详细答案", "20", "333333"},
		{"quick_ref", "速查", "18", "000000"},
		{"ai_note", "AI说明", "16", "CC7700"},
		{"separator", "分隔线", "18", "BFBFBF"},
		{"spacer", "空行", "10", "FFFFFF"},
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	b.WriteString(`<w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">`)
	for _, s := range styles {
		fmt.Fprintf(&b,
			`<w:style w:type="paragraph" w:styleId="%s"><w:name w:val="%s"/>
			<w:rPr><w:sz w:val="%s"/><w:color w:val="%s"/></w:rPr></w:style>`,
			s.id, s.name, s.sz, s.color)
	}
	b.WriteString(`</w:styles>`)
	return b.String()
}

func writeZip(zw *zip.Writer, name, content string) {
	w, _ := zw.Create(name)
	w.Write([]byte(content))
}

func examXMLEsc(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	var b strings.Builder
	for _, r := range s {
		if utf8.ValidRune(r) && (r == '\t' || r == '\n' || r == '\r' || r >= 0x20) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

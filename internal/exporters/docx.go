package exporters

// ExportDOCX writes questions to a Word .docx file using hand-crafted OOXML.
// The format is a ZIP containing:
//   - word/document.xml  (the body)
//   - word/styles.xml    (paragraph/character styles)
//   - [Content_Types].xml, _rels/.rels, word/_rels/document.xml.rels

import (
	"archive/zip"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/zyh001/med-exam-kit/internal/models"
)

// ExportDOCX writes questions to path (should end in .docx).
func ExportDOCX(questions []*models.Question, outPath string) error {
	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	defer zw.Close()

	docXML := buildDocumentXML(questions)

	writeZipFile(zw, "[Content_Types].xml", docxContentTypes())
	writeZipFile(zw, "_rels/.rels", docxRels())
	writeZipFile(zw, "word/_rels/document.xml.rels", docxWordRels())
	writeZipFile(zw, "word/styles.xml", docxStyles())
	writeZipFile(zw, "word/document.xml", docXML)
	return nil
}

func buildDocumentXML(questions []*models.Question) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	b.WriteString(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">`)
	b.WriteString(`<w:body>`)

	num := 0
	for _, q := range questions {
		for _, sq := range q.SubQuestions {
			// Question number heading
			num++
			b.WriteString(para("heading", fmt.Sprintf("第 %d 题　[%s]　%s", num, q.Mode, q.Unit), false))

			// Shared stem (A3/A4)
			if q.Stem != "" {
				b.WriteString(para("stem", "【题干】"+q.Stem, false))
			}
			// Question text
			if sq.Text != "" {
				b.WriteString(para("normal", sq.Text, false))
			}
			// Options
			for _, opt := range sq.Options {
				b.WriteString(para("option", "　　"+opt, false))
			}
			// Answer
			isAI := sq.AnswerSource() == "ai"
			answerLine := "【答案】" + sq.EffAnswer()
			if isAI {
				answerLine += "　(AI)"
			}
			b.WriteString(para("answer", answerLine, isAI))
			// Discussion
			if sq.EffDiscuss() != "" {
				discAI := sq.DiscussSource() == "ai"
				discLine := "【解析】" + sq.EffDiscuss()
				if discAI {
					discLine += "　(AI)"
				}
				b.WriteString(para("discuss", discLine, discAI))
			}
			// Rate
			if sq.Rate != "" {
				b.WriteString(para("meta", "正确率："+sq.Rate, false))
			}
			// Separator
			b.WriteString(para("separator", strings.Repeat("─", 32), false))
		}
	}

	b.WriteString(`<w:sectPr/>`)
	b.WriteString(`</w:body></w:document>`)
	return b.String()
}

// para returns a <w:p> element with text content.
// highlight=true adds a yellow background run property.
func para(style, text string, highlight bool) string {
	var b strings.Builder
	b.WriteString(`<w:p>`)
	b.WriteString(`<w:pPr><w:pStyle w:val="` + style + `"/></w:pPr>`)
	b.WriteString(`<w:r>`)
	if highlight {
		b.WriteString(`<w:rPr><w:highlight w:val="yellow"/></w:rPr>`)
	}
	b.WriteString(`<w:t xml:space="preserve">`)
	b.WriteString(xmlEscDoc(text))
	b.WriteString(`</w:t></w:r></w:p>`)
	return b.String()
}

func docxContentTypes() string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml"  ContentType="application/xml"/>
  <Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
  <Override PartName="/word/styles.xml"   ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.styles+xml"/>
</Types>`
}

func docxRels() string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
</Relationships>`
}

func docxWordRels() string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>
</Relationships>`
}

func docxStyles() string {
	// Minimal style definitions for the paragraph styles used above
	styles := []struct{ id, name, sz, color string }{
		{"heading", "题目编号", "24", "1F497D"},
		{"stem", "共享题干", "20", "595959"},
		{"normal", "题目文字", "22", "000000"},
		{"option", "选项", "20", "000000"},
		{"answer", "答案", "20", "C00000"},
		{"discuss", "解析", "20", "375623"},
		{"meta", "元数据", "18", "7F7F7F"},
		{"separator", "分隔线", "18", "BFBFBF"},
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

func writeZipFile(zw *zip.Writer, name, content string) {
	w, _ := zw.Create(name)
	w.Write([]byte(content))
}

func xmlEscDoc(s string) string {
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

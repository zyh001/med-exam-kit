package exporters

// ExportXLSX writes questions to an Excel file using a hand-crafted OOXML ZIP.
// This avoids external dependencies while supporting full Unicode (CJK) and
// cell colour highlights for AI-sourced fields.

import (
	"archive/zip"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/zyh001/med-exam-kit/internal/models"
)

// ExportXLSX writes questions to path (should end in .xlsx).
func ExportXLSX(questions []*models.Question, outPath string, splitOptions bool) error {
	rows, cols := Flatten(questions, splitOptions)

	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	defer zw.Close()

	// Build shared strings (deduplicated)
	ss, ssIdx := buildSharedStrings(rows, cols, ColumnDisplayName)

	writeFile(zw, "[Content_Types].xml", contentTypesXML())
	writeFile(zw, "_rels/.rels", relsXML())
	writeFile(zw, "xl/workbook.xml", workbookXML())
	writeFile(zw, "xl/_rels/workbook.xml.rels", workbookRelsXML())
	writeFile(zw, "xl/styles.xml", stylesXML())
	writeFile(zw, "xl/sharedStrings.xml", sharedStringsXML(ss))
	writeFile(zw, "xl/worksheets/sheet1.xml", sheetXML(rows, cols, ssIdx))
	return nil
}

// ── OOXML fragments ────────────────────────────────────────────────────

func contentTypesXML() string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
  <Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>
  <Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>
  <Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>
  <Override PartName="/xl/sharedStrings.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sharedStrings+xml"/>
</Types>`
}

func relsXML() string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>
</Relationships>`
}

func workbookXML() string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"
          xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <sheets>
    <sheet name="题目" sheetId="1" r:id="rId1"/>
  </sheets>
</workbook>`
}

func workbookRelsXML() string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>
  <Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>
  <Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/sharedStrings" Target="sharedStrings.xml"/>
</Relationships>`
}

// stylesXML defines 3 cell styles:
//
//	0 = normal
//	1 = bold header
//	2 = AI highlight (orange background)
func stylesXML() string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
  <fonts count="2">
    <font><sz val="10"/><name val="Arial"/></font>
    <font><b/><sz val="10"/><name val="Arial"/></font>
  </fonts>
  <fills count="3">
    <fill><patternFill patternType="none"/></fill>
    <fill><patternFill patternType="gray125"/></fill>
    <fill><patternFill patternType="solid"><fgColor rgb="FFFFD580"/></patternFill></fill>
  </fills>
  <borders count="1">
    <border><left/><right/><top/><bottom/><diagonal/></border>
  </borders>
  <cellStyleXfs count="1">
    <xf numFmtId="0" fontId="0" fillId="0" borderId="0"/>
  </cellStyleXfs>
  <cellXfs count="3">
    <xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/>
    <xf numFmtId="0" fontId="1" fillId="0" borderId="0" xfId="0"/>
    <xf numFmtId="0" fontId="0" fillId="2" borderId="0" xfId="0"/>
  </cellXfs>
</styleSheet>`
}

func sharedStringsXML(ss []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`+"\n")
	fmt.Fprintf(&b, `<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" count="%d" uniqueCount="%d">`, len(ss), len(ss))
	for _, s := range ss {
		fmt.Fprintf(&b, "<si><t xml:space=\"preserve\">%s</t></si>", xmlEsc(s))
	}
	b.WriteString("</sst>")
	return b.String()
}

func sheetXML(rows []Row, cols []string, ssIdx map[string]int) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	b.WriteString(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">`)
	b.WriteString(`<sheetView showGridLines="1" tabSelected="1" workbookViewId="0"/>`)
	b.WriteString("<sheetData>")

	// Header row (style 1 = bold)
	b.WriteString(`<row r="1">`)
	for ci, col := range cols {
		display := col
		if n, ok := ColumnDisplayName[col]; ok {
			display = n
		}
		b.WriteString(cellS(1, ci, 0, ssIdx[display], 1))
	}
	b.WriteString("</row>")

	// Data rows
	for ri, row := range rows {
		b.WriteString(fmt.Sprintf(`<row r="%d">`, ri+2))
		for ci, col := range cols {
			val := row[col]
			isAI := (col == "answer" && row["answer_source"] == "ai") ||
				(col == "discuss" && row["discuss_source"] == "ai")
			style := 0
			if isAI {
				style = 2 // orange
			}
			b.WriteString(cellS(ri+2, ci, 0, ssIdx[val], style))
		}
		b.WriteString("</row>")
	}

	b.WriteString("</sheetData></worksheet>")
	return b.String()
}

// cellS returns an XML cell element with a shared-string value and optional style.
func cellS(row, col, _, ssIndex, style int) string {
	addr := cellAddr(row, col)
	return fmt.Sprintf(`<c r="%s" t="s" s="%d"><v>%d</v></c>`, addr, style, ssIndex)
}

func cellAddr(row, col int) string {
	colStr := ""
	c := col
	for c >= 0 {
		colStr = string(rune('A'+c%26)) + colStr
		c = c/26 - 1
	}
	return fmt.Sprintf("%s%d", colStr, row)
}

// buildSharedStrings collects all unique string values (headers + data) in order.
func buildSharedStrings(rows []Row, cols []string, displayNames map[string]string) ([]string, map[string]int) {
	idx := map[string]int{}
	var ss []string

	add := func(s string) {
		if _, ok := idx[s]; !ok {
			idx[s] = len(ss)
			ss = append(ss, s)
		}
	}

	// Add header display names
	for _, col := range cols {
		name := col
		if n, ok := displayNames[col]; ok {
			name = n
		}
		add(name)
	}

	// Add all cell values
	for _, row := range rows {
		for _, col := range cols {
			add(row[col])
		}
	}
	return ss, idx
}

func xmlEsc(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	// Remove invalid XML characters
	var b strings.Builder
	for _, r := range s {
		if utf8.ValidRune(r) && (r == '\t' || r == '\n' || r == '\r' || r >= 0x20) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func writeFile(zw *zip.Writer, name, content string) {
	w, _ := zw.Create(name)
	w.Write([]byte(content))
}

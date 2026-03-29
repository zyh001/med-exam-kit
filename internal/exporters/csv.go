package exporters

import (
	"encoding/csv"
	"os"

	"github.com/med-exam-kit/med-exam-kit/internal/models"
)

// ExportCSV writes questions to a CSV file. Returns the output path.
func ExportCSV(questions []*models.Question, outPath string, splitOptions bool) error {
	rows, cols := Flatten(questions, splitOptions)

	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()

	// Write UTF-8 BOM so Excel opens it correctly on Windows
	f.WriteString("\xEF\xBB\xBF")

	w := csv.NewWriter(f)

	// Header row (Chinese display names)
	header := make([]string, len(cols))
	for i, c := range cols {
		if name, ok := ColumnDisplayName[c]; ok {
			header[i] = name
		} else {
			header[i] = c
		}
	}
	if err := w.Write(header); err != nil {
		return err
	}

	// Data rows
	for _, row := range rows {
		record := make([]string, len(cols))
		for i, c := range cols {
			record[i] = row[c]
		}
		if err := w.Write(record); err != nil {
			return err
		}
	}

	w.Flush()
	return w.Error()
}

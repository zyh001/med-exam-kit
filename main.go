package main

import (
	"embed"

	"github.com/med-exam-kit/med-exam-kit/cmd"
)

// Assets holds all frontend static files and HTML templates embedded into
// the binary at build time. Copy files from the Python project:
//
//	assets/static/    ← src/med_exam_toolkit/static/
//	assets/templates/ ← src/med_exam_toolkit/templates/
//
//go:embed assets
var assets embed.FS

func main() {
	cmd.Assets = assets
	cmd.Execute()
}

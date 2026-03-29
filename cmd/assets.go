package cmd

import "io/fs"

// Assets is set by main.go (embed.go) so the quiz server can serve
// the frontend files from the embedded filesystem.
// When nil, the server returns a 404 for the frontend.
var Assets fs.FS

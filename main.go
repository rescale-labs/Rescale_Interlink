// Wails entry point - used by wails build/dev commands.
//
// This package is at the project root for Wails tooling compatibility.
// For CLI builds, use: go build ./cmd/rescale-int
//
// Usage:
//   wails build     # Build GUI-only binary
//   wails dev       # Development mode with hot reload
//   go build ./cmd/rescale-int  # Build unified CLI+GUI binary
package main

import (
	"embed"
	"fmt"
	"os"

	"github.com/rescale/rescale-int/internal/wailsapp"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	wailsapp.Assets = assets
	if err := wailsapp.Run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

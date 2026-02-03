//go:build dev
// +build dev

// Package main provides a stub for frontend assets during development.
// In dev mode, wails dev serves assets directly without embedding.
package main

import "embed"

// Assets is an empty embed.FS for development builds.
// Use 'wails dev' for hot-reloading during development.
var Assets embed.FS

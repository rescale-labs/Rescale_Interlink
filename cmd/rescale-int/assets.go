//go:build !dev
// +build !dev

// Package main embeds the frontend assets for the Wails application.
// This file is only included in production builds (not with -tags dev).
// IMPORTANT: Use 'wails build' command which handles embedding automatically.
// Regular 'go build' will NOT work due to Go embed path limitations.
package main

import "embed"

// Assets will be populated by wails build command.
// The wails build process handles the embed directive automatically.
var Assets embed.FS

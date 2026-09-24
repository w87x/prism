package macos

import (
	_ "embed"
	"path/filepath"

	"prism/internal/swiftbin"
)

//go:embed eventkit/eventkit.swift
var eventkitSource []byte

//go:embed eventkit/Info.plist
var eventkitPlist []byte

// Helper is prism-eventkit, a small Swift program that talks to Calendar and Reminders through EventKit
// (AppleScript cannot expand recurring events). The source is embedded and compiled on first use.
type Helper = swiftbin.Tool

// NewHelper returns a helper that keeps its binary under dataDir/bin.
func NewHelper(dataDir string) *Helper {
	return &swiftbin.Tool{Name: "prism-eventkit", Source: eventkitSource, Plist: eventkitPlist, Dir: filepath.Join(dataDir, "bin")}
}

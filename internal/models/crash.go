package models

import "time"

// Crash holds the parsed metadata from a UE crash report submission.
type Crash struct {
	ID            int64
	GUID          string
	GameName      string
	Platform      string
	BuildVersion  string
	EngineVersion string
	CrashType     string // "Crash", "Assert", "Ensure", etc.
	ErrorMessage  string
	CallStack     string
	UserDesc      string
	ReceivedAt    time.Time
	Files         []CrashFile
}

// CrashFile represents one file attached to a crash report.
type CrashFile struct {
	ID       int64
	CrashID  int64
	Filename string
	SizeBytes int64
	// StorePath is relative to the configured data directory.
	StorePath string
}

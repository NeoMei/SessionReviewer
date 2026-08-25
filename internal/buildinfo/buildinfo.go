package buildinfo

import (
	"errors"
	"regexp"
	"runtime"
	"time"
)

var (
	Version = "dev"
	Commit  = "unknown"
	BuiltAt = "unknown"
)

type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuiltAt   string `json:"built_at"`
	GoVersion string `json:"go_version"`
}

func Current() Info {
	return Info{Version: Version, Commit: Commit, BuiltAt: BuiltAt, GoVersion: runtime.Version()}
}

func ValidateRelease(info Info) error {
	if !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(info.Version) {
		return errors.New("release version must be semantic without a v prefix")
	}
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(info.Commit) {
		return errors.New("release commit must be a full lowercase SHA-1")
	}
	if _, err := time.Parse(time.RFC3339, info.BuiltAt); err != nil {
		return errors.New("release build time must be RFC3339")
	}
	if info.GoVersion == "" {
		return errors.New("release Go version is required")
	}
	return nil
}

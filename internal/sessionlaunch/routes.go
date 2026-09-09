package sessionlaunch

import (
	"errors"
	"path/filepath"
	"regexp"
)

var nativeSessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$`)

func ProviderArguments(provider, nativeSessionID, projectRoot string) ([]string, error) {
	if !supportedProvider(provider) || !nativeSessionIDPattern.MatchString(nativeSessionID) || !cleanAbsolute(projectRoot) {
		return nil, errors.New("native Session route is invalid")
	}
	switch provider {
	case "codex":
		return []string{"resume", nativeSessionID}, nil
	case "claude":
		return []string{"--resume", nativeSessionID}, nil
	case "opencode":
		return []string{projectRoot, "--session", nativeSessionID}, nil
	default:
		panic("validated provider missing route")
	}
}

func supportedProvider(provider string) bool {
	return provider == "codex" || provider == "claude" || provider == "opencode"
}
func cleanAbsolute(value string) bool { return filepath.IsAbs(value) && filepath.Clean(value) == value }

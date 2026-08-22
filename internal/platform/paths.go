package platform

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

type Env struct {
	GOOS         string
	Home         string
	LocalAppData string
}

func CurrentEnv() Env {
	home, _ := os.UserHomeDir()
	return Env{
		GOOS:         runtime.GOOS,
		Home:         home,
		LocalAppData: os.Getenv("LOCALAPPDATA"),
	}
}

func DataDir(env Env) (string, error) {
	switch env.GOOS {
	case "darwin":
		if env.Home == "" {
			return "", fmt.Errorf("resolve data directory: home directory is empty")
		}
		return filepath.Join(env.Home, ".local", "share", "session-reviewer"), nil
	case "windows":
		if env.LocalAppData == "" {
			return "", fmt.Errorf("resolve data directory: LOCALAPPDATA is empty")
		}
		return filepath.Join(env.LocalAppData, "SessionReviewer"), nil
	default:
		return "", fmt.Errorf("unsupported operating system %q", env.GOOS)
	}
}

func NormalizePath(goos, value string) string {
	if goos == "windows" {
		value = strings.ReplaceAll(value, `\`, "/")
		return strings.ToLower(path.Clean(value))
	}
	return filepath.Clean(value)
}

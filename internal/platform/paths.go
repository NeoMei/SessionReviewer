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
		return normalizeWindowsPath(value)
	}
	return filepath.Clean(value)
}

func normalizeWindowsPath(value string) string {
	value = strings.ToLower(strings.ReplaceAll(value, `\`, "/"))
	if strings.HasPrefix(value, "//?/") || strings.HasPrefix(value, "//./") {
		prefix := value[:4]
		remainder := value[4:]
		if strings.HasPrefix(remainder, "unc/") {
			return prefix + "unc/" + cleanWindowsUNC(remainder[4:])
		}
		return prefix + cleanWindowsNonUNC(remainder)
	}
	if strings.HasPrefix(value, "//") {
		return "//" + cleanWindowsUNC(strings.TrimLeft(value, "/"))
	}
	return cleanWindowsNonUNC(value)
}

func cleanWindowsNonUNC(value string) string {
	if len(value) >= 2 && value[1] == ':' && isASCIILetter(value[0]) {
		drive := value[:2]
		if len(value) == 2 {
			return drive
		}
		if value[2] == '/' {
			return drive + path.Clean(value[2:])
		}
		remainder := path.Clean(value[2:])
		if remainder == "." {
			return drive
		}
		return drive + remainder
	}
	return path.Clean(value)
}

func cleanWindowsUNC(value string) string {
	parts := strings.FieldsFunc(value, func(r rune) bool { return r == '/' })
	if len(parts) <= 2 {
		return strings.Join(parts, "/")
	}
	root := strings.Join(parts[:2], "/")
	tail := strings.TrimPrefix(path.Clean("/"+strings.Join(parts[2:], "/")), "/")
	if tail == "" || tail == "." {
		return root
	}
	return root + "/" + tail
}

func isASCIILetter(value byte) bool {
	return value >= 'a' && value <= 'z'
}

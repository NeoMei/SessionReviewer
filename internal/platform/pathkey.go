package platform

import (
	"fmt"
	"path"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

type CaseMode string

const (
	CaseSensitive   CaseMode = "sensitive"
	CaseInsensitive CaseMode = "insensitive"
)

// PathKey returns the canonical identity of a safe relative Obsidian path.
// The display spelling remains separate from this key so NFC and case-only
// differences cannot create synchronization loops on insensitive volumes.
func PathKey(goos string, caseMode CaseMode, relative string) (string, error) {
	if caseMode != CaseSensitive && caseMode != CaseInsensitive {
		return "", fmt.Errorf("invalid filesystem case mode %q", caseMode)
	}
	canonical, err := validateRelativePath(goos, relative)
	if err != nil {
		return "", err
	}
	canonical = norm.NFC.String(canonical)
	if goos == "windows" || caseMode == CaseInsensitive {
		canonical = cases.Fold().String(canonical)
	}
	return canonical, nil
}

func validateRelativePath(goos, relative string) (string, error) {
	if relative == "" || !utf8.ValidString(relative) || strings.IndexByte(relative, 0) >= 0 {
		return "", fmt.Errorf("invalid relative path")
	}
	if relative[0] == '/' || relative[0] == '\\' || hasWindowsDrivePrefix(relative) {
		return "", fmt.Errorf("path must be relative")
	}
	canonical := strings.ReplaceAll(relative, `\`, "/")
	if strings.HasPrefix(canonical, "//") || path.Clean(canonical) != canonical || canonical == "." {
		return "", fmt.Errorf("path must be clean and relative")
	}
	for _, component := range strings.Split(canonical, "/") {
		if component == "" || component == "." || component == ".." {
			return "", fmt.Errorf("path contains an invalid component")
		}
		if strings.HasSuffix(component, ".") || (goos == "windows" && strings.HasSuffix(component, " ")) {
			return "", fmt.Errorf("path component has a trailing dot or space")
		}
		for _, r := range component {
			if r < 0x20 || r == 0x7f || strings.ContainsRune(`<>:"|?*`, r) {
				return "", fmt.Errorf("path component contains a forbidden character")
			}
		}
		if IsWindowsReservedName(component) {
			return "", fmt.Errorf("path contains a Windows reserved component")
		}
	}
	return canonical, nil
}

func hasWindowsDrivePrefix(value string) bool {
	return len(value) >= 2 && value[1] == ':' && ((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z'))
}

// IsWindowsReservedName reports whether a single path component is a Windows
// device name, including the superscript digit spellings recognized by Win32.
func IsWindowsReservedName(component string) bool {
	base := component
	if dot := strings.IndexByte(base, '.'); dot >= 0 {
		base = base[:dot]
	}
	base = asciiUpper(base)
	if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" {
		return true
	}
	runes := []rune(base)
	if len(runes) != 4 || (string(runes[:3]) != "COM" && string(runes[:3]) != "LPT") {
		return false
	}
	digit := runes[3]
	return (digit >= '1' && digit <= '9') || digit == '¹' || digit == '²' || digit == '³'
}

func asciiUpper(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))
	for index := 0; index < len(value); index++ {
		b := value[index]
		if b >= 'a' && b <= 'z' {
			b -= 'a' - 'A'
		}
		builder.WriteByte(b)
	}
	return builder.String()
}

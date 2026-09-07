package redact

import "strings"

// AbsolutePaths replaces delimited Unix, UNC, Windows drive, and file URL
// paths without treating closing markup as a path.
func AbsolutePaths(value string) string {
	const marker = "[REDACTED:ABSOLUTE_PATH]"
	var result strings.Builder
	wrote, copied := false, 0
	for start := 0; start < len(value); start++ {
		pathStart := start
		if strings.HasPrefix(value[start:], "file://") && start+7 < len(value) && absolutePathStart(value, start+7) && absolutePathBoundary(value, start) {
			pathStart = start + 7
		} else if !absolutePathStart(value, start) || !absolutePathBoundary(value, start) {
			continue
		}
		end := pathStart
		quote := byte(0)
		if start > 0 && (value[start-1] == '\'' || value[start-1] == '"') {
			quote = value[start-1]
		}
		for end < len(value) {
			if quote != 0 {
				if value[end] == quote {
					break
				}
			} else if end > start && (value[end] == ' ' || value[end] == '\t' || value[end] == '\r' || value[end] == '\n' || strings.ContainsRune(",;)]}<>", rune(value[end]))) {
				break
			}
			end++
		}
		if end == start {
			continue
		}
		result.WriteString(value[copied:start])
		result.WriteString(marker)
		copied, start, wrote = end, end-1, true
	}
	if !wrote {
		return value
	}
	result.WriteString(value[copied:])
	return result.String()
}

func absolutePathStart(value string, offset int) bool {
	if value[offset] == '/' {
		if closingMarkupAt(value, offset) {
			return false
		}
		return offset+1 < len(value) && value[offset+1] != '/'
	}
	if value[offset] == '\\' {
		return offset+2 < len(value) && value[offset+1] == '\\'
	}
	return offset+3 < len(value) && ((value[offset] >= 'A' && value[offset] <= 'Z') || (value[offset] >= 'a' && value[offset] <= 'z')) && value[offset+1] == ':' && (value[offset+2] == '/' || value[offset+2] == '\\')
}

func closingMarkupAt(value string, slash int) bool {
	if slash == 0 || value[slash-1] != '<' {
		return false
	}
	end := strings.IndexByte(value[slash+1:], '>')
	if end < 1 {
		return false
	}
	name := value[slash+1 : slash+1+end]
	for index := range name {
		current := name[index]
		if !((current >= 'a' && current <= 'z') || (current >= 'A' && current <= 'Z') || (current >= '0' && current <= '9') || current == '_' || current == '-' || current == ':') {
			return false
		}
	}
	return true
}

func absolutePathBoundary(value string, offset int) bool {
	if offset == 0 {
		return true
	}
	previous := value[offset-1]
	return previous == ' ' || previous == '\t' || previous == '\r' || previous == '\n' || strings.ContainsRune(":\"'([{<=>", rune(previous))
}

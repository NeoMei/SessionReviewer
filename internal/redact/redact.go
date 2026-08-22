package redact

import (
	"math"
	"regexp"
	"sort"
	"strings"
)

type Finding struct {
	Rule  string `json:"rule"`
	Count int    `json:"count"`
}

type Result struct {
	Text     string    `json:"text"`
	Findings []Finding `json:"findings,omitempty"`
}

type rule struct {
	name string
	re   *regexp.Regexp
}

type Redactor struct {
	rules []rule
}

type textSegment struct {
	text string
	// protected segments are emitted markers and are never scanned by later rules.
	protected bool
}

var (
	tokenCandidate        = regexp.MustCompile(`[A-Za-z0-9+/=_-]{40,}`)
	stableID              = regexp.MustCompile(`^(?:msg_|rs_|ctc_|ctco_|ev-)(?:[A-Za-z0-9]{40}|[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12})$`)
	namedSecretAssignment = regexp.MustCompile(`\b"?(?:(?i:(?:[A-Z0-9]+[_-])*(?:api[_-]?key|access[_-]?token|auth(?:orization)?|cookie|password|secret))|(?:[a-z][A-Za-z0-9]*(?:ApiKey|AccessToken|AuthToken|Authorization|Cookie|Password|Secret)))"?\s*[:=][ \t]*`)
	nextFieldBoundary     = regexp.MustCompile(`(?:[,;][ \t]*|\r?\n[ \t]*|[ \t]+)"?[A-Za-z_][A-Za-z0-9_.-]*"?[ \t]*[:=]`)
)

func Default() Redactor {
	return Redactor{rules: []rule{
		{"private_key", regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----(?:.*?-----END [A-Z ]*PRIVATE KEY-----|.*\z)`)},
		{"bearer", regexp.MustCompile(`(?i)Bearer\s+[A-Za-z0-9._~+/=-]{12,}`)},
		{"openai_key", regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}\b`)},
		{"connection_url", regexp.MustCompile(`(?i)\b[A-Z][A-Z0-9+.-]*://[^\s/@:]+:[^\s/@]+@[^\s]+`)},
	}}
}

func (r Redactor) Text(input string) Result {
	counts := map[string]int{}
	segments := []textSegment{{text: input}}
	for _, rule := range r.rules {
		segments = applyRegexpRule(segments, rule, counts)
	}
	segments = applyNamedSecretRule(segments, counts)

	for i := range segments {
		if segments[i].protected {
			continue
		}
		segments[i].text = tokenCandidate.ReplaceAllStringFunc(segments[i].text, func(value string) string {
			if isStableID(value) || entropy(value) < 4.0 {
				return value
			}
			counts["high_entropy_token"]++
			return "[REDACTED:HIGH_ENTROPY_TOKEN]"
		})
	}

	var text strings.Builder
	for _, segment := range segments {
		text.WriteString(segment.text)
	}

	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	result := Result{Text: text.String()}
	for _, key := range keys {
		result.Findings = append(result.Findings, Finding{Rule: key, Count: counts[key]})
	}
	return result
}

func applyRegexpRule(segments []textSegment, current rule, counts map[string]int) []textSegment {
	result := make([]textSegment, 0, len(segments))
	marker := "[REDACTED:" + strings.ToUpper(current.name) + "]"
	for _, segment := range segments {
		if segment.protected {
			result = append(result, segment)
			continue
		}
		matches := current.re.FindAllStringIndex(segment.text, -1)
		if len(matches) == 0 {
			result = append(result, segment)
			continue
		}
		counts[current.name] += len(matches)
		cursor := 0
		for _, match := range matches {
			appendSegment(&result, segment.text[cursor:match[0]], false)
			appendSegment(&result, marker, true)
			cursor = match[1]
		}
		appendSegment(&result, segment.text[cursor:], false)
	}
	return result
}

func applyNamedSecretRule(segments []textSegment, counts map[string]int) []textSegment {
	result := make([]textSegment, 0, len(segments))
	for i, segment := range segments {
		if segment.protected {
			result = append(result, segment)
			continue
		}
		followedByProtected := i+1 < len(segments) && segments[i+1].protected
		cursor := 0
		for cursor < len(segment.text) {
			assignment := namedSecretAssignment.FindStringIndex(segment.text[cursor:])
			if assignment == nil {
				break
			}
			start := cursor + assignment[0]
			valueStart := cursor + assignment[1]
			end, complete := namedSecretValueEnd(segment.text, valueStart)
			if !complete && end == len(segment.text) && followedByProtected {
				break
			}
			appendSegment(&result, segment.text[cursor:start], false)
			appendSegment(&result, "[REDACTED:NAMED_SECRET]", true)
			counts["named_secret"]++
			cursor = end
		}
		appendSegment(&result, segment.text[cursor:], false)
	}
	return result
}

func namedSecretValueEnd(text string, start int) (int, bool) {
	if start >= len(text) {
		return start, false
	}
	switch text[start] {
	case '"', '\'':
		if end := quotedValueEnd(text, start, text[start]); end >= 0 {
			return end, true
		}
	case '[':
		if end := structuredValueEnd(text, start, '[', ']'); end >= 0 {
			return end, true
		}
	case '{':
		if end := structuredValueEnd(text, start, '{', '}'); end >= 0 {
			return end, true
		}
	default:
		if end := safeValueBoundary(text, start); end < len(text) {
			return end, true
		}
		return len(text), true
	}
	if end := safeValueBoundary(text, start+1); end < len(text) {
		return end, true
	}
	return len(text), false
}

func quotedValueEnd(text string, start int, quote byte) int {
	escaped := false
	for i := start + 1; i < len(text); i++ {
		if escaped {
			escaped = false
			continue
		}
		if text[i] == '\\' {
			escaped = true
			continue
		}
		if text[i] == quote {
			return i + 1
		}
	}
	return -1
}

func structuredValueEnd(text string, start int, open, close byte) int {
	depth := 0
	var quote byte
	escaped := false
	for i := start; i < len(text); i++ {
		if quote != 0 {
			if escaped {
				escaped = false
				continue
			}
			if text[i] == '\\' {
				escaped = true
				continue
			}
			if text[i] == quote {
				quote = 0
			}
			continue
		}
		switch text[i] {
		case '"', '\'':
			quote = text[i]
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}

func safeValueBoundary(text string, start int) int {
	// Malformed values remain secret until a structurally clear next field or
	// container close. Whitespace and control bytes alone are not safe boundaries.
	boundary := len(text)
	if match := nextFieldBoundary.FindStringIndex(text[start:]); match != nil {
		boundary = start + match[0]
	}
	for i := start; i < boundary; i++ {
		if text[i] == '}' || text[i] == ']' {
			return i
		}
	}
	return boundary
}

func appendSegment(segments *[]textSegment, text string, protected bool) {
	if text == "" {
		return
	}
	*segments = append(*segments, textSegment{text: text, protected: protected})
}

func isStableID(value string) bool {
	return stableID.MatchString(value)
}

func entropy(value string) float64 {
	counts := map[rune]float64{}
	runes := []rune(value)
	for _, r := range runes {
		counts[r]++
	}

	var result float64
	for _, count := range counts {
		p := count / float64(len(runes))
		result -= p * math.Log2(p)
	}
	return result
}

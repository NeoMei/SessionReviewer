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
	stableID              = regexp.MustCompile(`^(?:(?:msg_|rs_|ctc_|ctco_|ev-)(?:[A-Za-z0-9]{40}|[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12})|session-report-[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12})$`)
	bearerCredential      = regexp.MustCompile(`(?i)Bearer\s+[A-Za-z0-9._~+/=-]{12,}`)
	privateKeyBegin       = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)
	namedSecretAssignment = regexp.MustCompile(`\b"?(?:(?i:(?:[A-Z0-9]+[_-])*(?:api[_-]?key|access[_-]?token|auth(?:orization)?|cookie|password|secret))|(?:[a-z][A-Za-z0-9]*(?:ApiKey|AccessToken|AuthToken|Authorization|Cookie|Password|Secret)))"?\s*[:=][ \t]*`)
	nextFieldBoundary     = regexp.MustCompile(`(?:[,;][ \t]*|\r?\n[ \t]*|[ \t]+)"?[A-Za-z_][A-Za-z0-9_.-]*"?[ \t]*[:=]`)
)

func Default() Redactor {
	return Redactor{rules: []rule{
		{"bearer", bearerCredential},
		{"openai_key", regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}\b`)},
		{"connection_url", regexp.MustCompile(`(?i)\b[A-Z][A-Z0-9+.-]*://[^\s/@:]+:[^\s/@]+@[^\s]+`)},
	}}
}

func (r Redactor) Text(input string) Result {
	counts := map[string]int{}
	segments := []textSegment{{text: input}}
	segments = applyNamedSecretRule(segments, counts)
	segments = applyPrivateKeyRule(segments, counts)
	for _, rule := range r.rules {
		segments = applyRegexpRule(segments, rule, counts)
	}

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

func applyPrivateKeyRule(segments []textSegment, counts map[string]int) []textSegment {
	result := make([]textSegment, 0, len(segments))
	for _, segment := range segments {
		if segment.protected {
			result = append(result, segment)
			continue
		}
		cursor := 0
		for cursor < len(segment.text) {
			begin := privateKeyBegin.FindStringIndex(segment.text[cursor:])
			if begin == nil {
				break
			}
			start := cursor + begin[0]
			end, _, _ := privateKeyEnvelopeEnd(segment.text, start)
			appendSegment(&result, segment.text[cursor:start], false)
			appendSegment(&result, "[REDACTED:PRIVATE_KEY]", true)
			counts["private_key"]++
			cursor = end
		}
		appendSegment(&result, segment.text[cursor:], false)
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
	for _, segment := range segments {
		if segment.protected {
			result = append(result, segment)
			continue
		}
		emitted := 0
		search := 0
		for search < len(segment.text) {
			assignment := namedSecretAssignment.FindStringIndex(segment.text[search:])
			if assignment == nil {
				break
			}
			start := search + assignment[0]
			valueStart := search + assignment[1]
			end, _ := namedSecretValueEnd(segment.text, valueStart)
			if prefersBearerRule(segment.text[start:valueStart], segment.text[valueStart:end]) {
				search = end
				continue
			}
			appendSegment(&result, segment.text[emitted:start], false)
			appendSegment(&result, "[REDACTED:NAMED_SECRET]", true)
			counts["named_secret"]++
			emitted = end
			search = end
		}
		appendSegment(&result, segment.text[emitted:], false)
	}
	return result
}

func prefersBearerRule(assignment, value string) bool {
	separator := strings.LastIndexAny(assignment, ":=")
	if separator < 0 {
		return false
	}
	key := strings.ToLower(strings.Trim(strings.TrimSpace(assignment[:separator]), `"'`))
	isAuthorization := key == "auth" || key == "authorization" ||
		strings.HasSuffix(key, "_auth") || strings.HasSuffix(key, "-auth") ||
		strings.HasSuffix(key, "_authorization") || strings.HasSuffix(key, "-authorization") ||
		strings.HasSuffix(key, "authorization")
	if !isAuthorization {
		return false
	}
	value = strings.TrimLeft(value, " \t")
	var quote byte
	if len(value) > 0 && (value[0] == '"' || value[0] == '\'') {
		quote = value[0]
		value = value[1:]
	}
	match := bearerCredential.FindStringIndex(value)
	if match == nil || match[0] != 0 {
		return false
	}
	remainder := value[match[1]:]
	if quote != 0 {
		return len(remainder) > 0 && remainder[0] == quote && strings.TrimSpace(remainder[1:]) == ""
	}
	return strings.TrimSpace(remainder) == ""
}

func namedSecretValueEnd(text string, start int) (int, bool) {
	if start >= len(text) {
		return start, false
	}
	pemRanges := privateKeyEnvelopeRanges(text, start)
	switch text[start] {
	case '"', '\'':
		if end := quotedValueEnd(text, start, text[start], pemRanges); end >= 0 {
			return end, true
		}
	case '[':
		if end := structuredValueEnd(text, start, '[', ']', pemRanges); end >= 0 {
			return end, true
		}
	case '{':
		if end := structuredValueEnd(text, start, '{', '}', pemRanges); end >= 0 {
			return end, true
		}
	default:
		if end := safeValueBoundary(text, start, pemRanges); end < len(text) {
			return end, true
		}
		return len(text), true
	}
	if end := safeValueBoundary(text, start+1, pemRanges); end < len(text) {
		return end, true
	}
	return len(text), false
}

func privateKeyEnvelopeEnd(text string, start int) (int, bool, bool) {
	begin := privateKeyBegin.FindStringIndex(text[start:])
	if begin == nil || begin[0] != 0 {
		return start, false, false
	}
	headerEnd := start + begin[1]
	header := text[start:headerEnd]
	label := strings.TrimSuffix(strings.TrimPrefix(header, "-----BEGIN "), "-----")
	endMarker := "-----END " + label + "-----"
	if offset := strings.Index(text[headerEnd:], endMarker); offset >= 0 {
		return headerEnd + offset + len(endMarker), true, true
	}
	return len(text), true, false
}

func quotedValueEnd(text string, start int, quote byte, pemRanges []textRange) int {
	escaped := false
	for i := start + 1; i < len(text); i++ {
		if end, ok := protectedRangeEndAt(i, pemRanges); ok {
			i = end - 1
			escaped = false
			continue
		}
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

func structuredValueEnd(text string, start int, open, close byte, pemRanges []textRange) int {
	depth := 0
	var quote byte
	escaped := false
	for i := start; i < len(text); i++ {
		if end, ok := protectedRangeEndAt(i, pemRanges); ok {
			i = end - 1
			escaped = false
			continue
		}
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

func safeValueBoundary(text string, start int, pemRanges []textRange) int {
	// Malformed values remain secret until a structurally clear next field or
	// container close. Whitespace and control bytes alone are not safe boundaries.
	boundary := len(text)
	for _, match := range nextFieldBoundary.FindAllStringIndex(text[start:], -1) {
		candidate := start + match[0]
		if indexInRanges(candidate, pemRanges) {
			continue
		}
		matchEnd := start + match[1]
		if matchEnd+1 < len(text) && text[matchEnd-1] == ':' && text[matchEnd:matchEnd+2] == "//" {
			continue
		}
		boundary = candidate
		break
	}
	for i := start; i < boundary; i++ {
		if (text[i] == '}' || text[i] == ']') && !indexInRanges(i, pemRanges) {
			return i
		}
	}
	return boundary
}

type textRange struct {
	start int
	end   int
}

func privateKeyEnvelopeRanges(text string, start int) []textRange {
	var ranges []textRange
	cursor := start
	for cursor < len(text) {
		begin := privateKeyBegin.FindStringIndex(text[cursor:])
		if begin == nil {
			break
		}
		pemStart := cursor + begin[0]
		pemEnd, _, complete := privateKeyEnvelopeEnd(text, pemStart)
		ranges = append(ranges, textRange{start: pemStart, end: pemEnd})
		if !complete {
			break
		}
		cursor = pemEnd
	}
	return ranges
}

func indexInRanges(index int, ranges []textRange) bool {
	for _, current := range ranges {
		if index >= current.start && index < current.end {
			return true
		}
	}
	return false
}

func protectedRangeEndAt(index int, ranges []textRange) (int, bool) {
	for _, current := range ranges {
		if index == current.start {
			return current.end, true
		}
	}
	return 0, false
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

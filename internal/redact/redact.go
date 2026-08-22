package redact

import (
	"math"
	"regexp"
	"sort"
	"strconv"
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

var (
	tokenCandidate = regexp.MustCompile(`[A-Za-z0-9+/=_-]{40,}`)
	stableID       = regexp.MustCompile(`^(?:msg_|rs_|ctc_|ctco_|ev-)(?:[A-Za-z0-9]{40}|[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12})$`)
)

func Default() Redactor {
	return Redactor{rules: []rule{
		{"private_key", regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----(?:.*?-----END [A-Z ]*PRIVATE KEY-----|.*\z)`)},
		{"bearer", regexp.MustCompile(`(?i)Bearer\s+[A-Za-z0-9._~+/=-]{12,}`)},
		{"openai_key", regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}\b`)},
		{"connection_url", regexp.MustCompile(`(?i)\b[A-Z][A-Z0-9+.-]*://[^\x00\s/@:]+:[^\x00\s/@]+@[^\x00\s]+`)},
		{"named_secret", regexp.MustCompile(`\b"?(?:(?i:(?:[A-Z0-9]+[_-])*(?:api[_-]?key|access[_-]?token|auth(?:orization)?|cookie|password|secret))|(?:[a-z][A-Za-z0-9]*(?:ApiKey|AccessToken|AuthToken|Authorization|Cookie|Password|Secret)))"?\s*[:=]\s*(?:"(?:\\.|[^\x00"\\\r\n])*(?:"|(?m:$))|'(?:\\.|[^\x00'\\\r\n])*(?:'|(?m:$))|\[[^\x00\]\r\n]*(?:\]|(?m:$))|\{[^\x00}\r\n]*(?:}|(?m:$))|[^\x00\s,;\[{"']+)`)},
	}}
}

func (r Redactor) Text(input string) Result {
	text := input
	counts := map[string]int{}
	placeholderStem := "\x00SESSION_REVIEWER_REDACTION"
	for strings.Contains(input, placeholderStem) {
		placeholderStem += "_"
	}
	type replacement struct {
		placeholder string
		marker      string
	}
	replacements := make([]replacement, 0, len(r.rules))
	for _, rule := range r.rules {
		matches := rule.re.FindAllStringIndex(text, -1)
		if len(matches) == 0 {
			continue
		}
		counts[rule.name] += len(matches)
		placeholder := placeholderStem + strconv.Itoa(len(replacements)) + "\x00"
		marker := "[REDACTED:" + strings.ToUpper(rule.name) + "]"
		replacements = append(replacements, replacement{placeholder: placeholder, marker: marker})
		text = rule.re.ReplaceAllString(text, placeholder)
	}
	for _, replacement := range replacements {
		text = strings.ReplaceAll(text, replacement.placeholder, replacement.marker)
	}

	text = tokenCandidate.ReplaceAllStringFunc(text, func(value string) string {
		if isStableID(value) || entropy(value) < 4.0 {
			return value
		}
		counts["high_entropy_token"]++
		return "[REDACTED:HIGH_ENTROPY_TOKEN]"
	})

	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	result := Result{Text: text}
	for _, key := range keys {
		result.Findings = append(result.Findings, Finding{Rule: key, Count: counts[key]})
	}
	return result
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

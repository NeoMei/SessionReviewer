package syncdoc

import (
	"bytes"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// encodeV4YAMLNode leaves the legacy unit encoding contract alone. yaml.v3's
// block emitter can add a terminal LF to folded keep scalars, or lose one from
// blank-only block scalars. Correct only an observed, exact typed-value delta,
// against the emitted scalar's own body, then verify the complete typed tree.
func encodeV4YAMLNode(node *yaml.Node) ([]byte, error) {
	source := encodeNode(node)
	if len(source) == 0 {
		return nil, invalidDocument("cannot encode v4 Markdown YAML node")
	}
	if !v4YAMLHasBlockScalar(node) {
		return source, nil
	}
	if len(source) > maxFrontmatterBytes {
		return nil, invalidDocument("encoded v4 Markdown YAML exceeds size limit")
	}
	emitted, err := decodeUnitValue(source)
	if err != nil {
		return nil, err
	}
	lines := []int{0}
	for index, value := range source {
		if value == '\n' {
			lines = append(lines, index+1)
		}
	}
	nodeLines := make([]int, 0)
	var collectLines func(*yaml.Node)
	collectLines = func(n *yaml.Node) {
		nodeLines = append(nodeLines, n.Line)
		for _, child := range n.Content {
			collectLines(child)
		}
	}
	collectLines(emitted)
	sort.Ints(nodeLines)
	var edits []v4SourceEdit
	var compare func(*yaml.Node, *yaml.Node) error
	compare = func(want, got *yaml.Node) error {
		if want.Kind != got.Kind || want.Tag != got.Tag || len(want.Content) != len(got.Content) {
			return invalidDocument("v4 Markdown YAML encoding changed typed structure")
		}
		if want.Value != got.Value {
			extra := want.Kind == yaml.ScalarNode && want.Style&yaml.FoldedStyle != 0 && strings.HasSuffix(want.Value, "\n\n") && got.Value == want.Value+"\n"
			missing := want.Kind == yaml.ScalarNode && want.Style&(yaml.FoldedStyle|yaml.LiteralStyle) != 0 && want.Value != "" && strings.Trim(want.Value, "\n") == "" && got.Value == strings.TrimSuffix(want.Value, "\n")
			if (!extra && !missing) || got.Style&(yaml.FoldedStyle|yaml.LiteralStyle) != want.Style&(yaml.FoldedStyle|yaml.LiteralStyle) {
				return invalidDocument("v4 Markdown YAML encoding changed scalar value")
			}
			end, ok := v4EncodedBlockEnd(source, lines, nodeLines, got)
			if !ok {
				return invalidDocument("cannot locate v4 Markdown encoded block scalar")
			}
			if extra {
				start := bytes.LastIndexByte(source[:end-1], '\n') + 1
				if start >= end || len(bytes.TrimSpace(source[start:end])) != 0 {
					return invalidDocument("cannot locate excess v4 Markdown scalar blank")
				}
				edits = append(edits, v4SourceEdit{start: start, end: end})
			} else {
				edits = append(edits, v4SourceEdit{start: end, end: end, value: []byte("\n")})
			}
		}
		for index := range want.Content {
			if err := compare(want.Content[index], got.Content[index]); err != nil {
				return err
			}
		}
		return nil
	}
	if err := compare(node, emitted); err != nil {
		return nil, err
	}
	if len(edits) == 0 {
		return source, nil
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].start < edits[j].start })
	parts := make([][]byte, 0, len(edits)*2+1)
	cursor := 0
	for _, edit := range edits {
		if edit.start < cursor || edit.end < edit.start || edit.end > len(source) {
			return nil, invalidDocument("overlapping v4 Markdown scalar encoding repairs")
		}
		parts = append(parts, source[cursor:edit.start], edit.value)
		cursor = edit.end
	}
	parts = append(parts, source[cursor:])
	result, err := joinV4Bounded(parts, maxFrontmatterBytes)
	if err != nil {
		return nil, err
	}
	verified, err := decodeUnitValue(result)
	if err != nil || !v4YAMLTypedEqual(node, verified) || !v4YAMLCommentsEqual(emitted, verified) {
		return nil, invalidDocument("v4 Markdown scalar encoding repair changed YAML content")
	}
	return result, nil
}

func v4YAMLHasBlockScalar(node *yaml.Node) bool {
	if node.Kind == yaml.ScalarNode && node.Style&(yaml.FoldedStyle|yaml.LiteralStyle) != 0 {
		return true
	}
	for _, child := range node.Content {
		if v4YAMLHasBlockScalar(child) {
			return true
		}
	}
	return false
}

func v4EncodedBlockEnd(source []byte, lines, nodeLines []int, node *yaml.Node) (int, bool) {
	if node.Line < 1 || node.Line >= len(lines) {
		return 0, false
	}
	limit := len(source)
	next := sort.SearchInts(nodeLines, node.Line+1)
	if next < len(nodeLines) {
		limit = lines[nodeLines[next]-1]
	}
	end, indent := lines[node.Line], -1
	for index := node.Line; index+1 < len(lines) && lines[index] < limit; index++ {
		line := source[lines[index]:lines[index+1]]
		trimmed := bytes.TrimLeft(line, " ")
		if len(bytes.TrimSpace(line)) != 0 {
			if strings.Trim(node.Value, "\n") == "" {
				break
			}
			spaces := len(line) - len(trimmed)
			if indent == -1 {
				indent = spaces
			}
			if spaces < indent {
				break
			}
		}
		end = lines[index+1]
	}
	return end, end > 0 && end <= len(source) && source[end-1] == '\n'
}

func v4YAMLTypedEqual(a, b *yaml.Node) bool {
	if a.Kind != b.Kind || a.Tag != b.Tag || a.Value != b.Value || len(a.Content) != len(b.Content) {
		return false
	}
	for i := range a.Content {
		if !v4YAMLTypedEqual(a.Content[i], b.Content[i]) {
			return false
		}
	}
	return true
}

func v4YAMLCommentsEqual(a, b *yaml.Node) bool {
	if a.HeadComment != b.HeadComment || a.LineComment != b.LineComment || a.FootComment != b.FootComment || len(a.Content) != len(b.Content) {
		return false
	}
	for i := range a.Content {
		if !v4YAMLCommentsEqual(a.Content[i], b.Content[i]) {
			return false
		}
	}
	return true
}

package codex

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strconv"
	"strings"
)

var processExitCodePattern = regexp.MustCompile(`^Process exited with code (-?[0-9]+)$`)

type toolCallEnvelope struct {
	callID string
	name   string
	input  string
}

type terminalOutput struct {
	body     string
	exitCode *int
	finished bool
}

func decodeToolCallEnvelope(raw json.RawMessage) (toolCallEnvelope, error) {
	var payload struct {
		Type      string `json:"type"`
		ID        string `json:"id"`
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Input     string `json:"input"`
		Arguments string `json:"arguments"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return toolCallEnvelope{}, errors.New("malformed tool call")
	}
	callID := payload.CallID
	if callID == "" {
		callID = payload.ID
	}
	switch payload.Type {
	case "custom_tool_call":
		return toolCallEnvelope{callID: callID, name: payload.Name, input: payload.Input}, nil
	case "function_call":
		return toolCallEnvelope{callID: callID, name: payload.Name, input: payload.Arguments}, nil
	default:
		return toolCallEnvelope{}, errors.New("unsupported tool call envelope")
	}
}

func decodeTerminalOutput(raw json.RawMessage) (terminalOutput, error) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		trimmed := strings.TrimSpace(text)
		if strings.HasPrefix(trimmed, "{") {
			if object, err := decodeUniqueJSONObject([]byte(trimmed)); err == nil {
				return decodeStructuredTerminalOutput(object)
			} else {
				return terminalOutput{}, err
			}
		}
		return decodeTextTerminalOutput(text)
	}
	if object, err := decodeUniqueJSONObject(raw); err == nil {
		return decodeStructuredTerminalOutput(object)
	} else if len(bytes.TrimSpace(raw)) > 0 && bytes.TrimSpace(raw)[0] == '{' {
		return terminalOutput{}, err
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return terminalOutput{}, errors.New("unsupported tool output envelope")
	}
	var parts []string
	for _, block := range blocks {
		if (block.Type == "text" || block.Type == "input_text" || block.Type == "output_text") && block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return decodeTextTerminalOutput(strings.Join(parts, "\n"))
}

func decodeStructuredTerminalOutput(object map[string]json.RawMessage) (terminalOutput, error) {
	body := ""
	if raw, found := object["output"]; found && json.Unmarshal(raw, &body) != nil {
		return terminalOutput{}, errors.New("malformed terminal output body")
	}
	rawExit, exitFound := object["exit_code"]
	rawSession, sessionFound := object["session_id"]
	if !exitFound && !sessionFound {
		return terminalOutput{body: body}, nil
	}
	liveSession := sessionFound && !bytes.Equal(bytes.TrimSpace(rawSession), []byte("null"))
	if liveSession {
		var session any
		if json.Unmarshal(rawSession, &session) != nil {
			return terminalOutput{}, errors.New("malformed terminal session")
		}
	}
	if !exitFound || bytes.Equal(bytes.TrimSpace(rawExit), []byte("null")) {
		return terminalOutput{body: body}, nil
	}
	var exitCode int
	if json.Unmarshal(rawExit, &exitCode) != nil || liveSession {
		return terminalOutput{}, errors.New("malformed or contradictory terminal metadata")
	}
	return terminalOutput{body: body, exitCode: &exitCode, finished: true}, nil
}

func decodeTextTerminalOutput(output string) (terminalOutput, error) {
	lines := normalizedLines(output)
	delimiter := -1
	for index, line := range lines {
		if line == "Output:" {
			delimiter = index
			break
		}
	}
	if delimiter >= 0 {
		codes, err := collectExitCodes(lines[:delimiter], processExitCodePattern, exitCodePattern)
		if err != nil {
			return terminalOutput{}, err
		}
		if len(codes) == 1 {
			return terminalOutput{body: strings.Join(lines[delimiter+1:], "\n"), exitCode: &codes[0], finished: true}, nil
		}
		return terminalOutput{body: strings.Join(lines[delimiter+1:], "\n")}, nil
	}
	codes, err := collectExitCodes(lines, exitCodePattern)
	if err != nil {
		return terminalOutput{}, err
	}
	bodyLines := make([]string, 0, len(lines))
	for _, line := range lines {
		if !exitCodePattern.MatchString(line) {
			bodyLines = append(bodyLines, line)
		}
	}
	if len(codes) == 1 {
		return terminalOutput{body: strings.Join(bodyLines, "\n"), exitCode: &codes[0], finished: true}, nil
	}
	return terminalOutput{body: output}, nil
}

func decodeUniqueJSONObject(raw []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("malformed terminal object")
	}
	object := make(map[string]json.RawMessage)
	for decoder.More() {
		keyToken, err := decoder.Token()
		key, ok := keyToken.(string)
		if err != nil || !ok {
			return nil, errors.New("malformed terminal object key")
		}
		if _, duplicate := object[key]; duplicate {
			return nil, errors.New("duplicate terminal object field")
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, errors.New("malformed terminal object value")
		}
		object[key] = value
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, errors.New("malformed terminal object")
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return nil, errors.New("trailing terminal object data")
	}
	return object, nil
}

func collectExitCodes(lines []string, patterns ...*regexp.Regexp) ([]int, error) {
	var found []int
	for _, line := range lines {
		for _, pattern := range patterns {
			match := pattern.FindStringSubmatch(line)
			if match == nil {
				continue
			}
			value, err := strconv.Atoi(match[1])
			if err != nil {
				return nil, errors.New("invalid terminal exit code")
			}
			if len(found) == 0 || found[len(found)-1] != value {
				found = append(found, value)
			}
			break
		}
	}
	if len(found) > 1 {
		return nil, errors.New("conflicting terminal exit codes")
	}
	return found, nil
}

func decodeExecCommandInput(input string) (cmd, workdir string, valid bool) {
	var payload struct {
		Cmd     string `json:"cmd"`
		Workdir string `json:"workdir"`
	}
	if json.Unmarshal([]byte(input), &payload) != nil || strings.TrimSpace(payload.Cmd) == "" {
		return "", "", false
	}
	return payload.Cmd, payload.Workdir, true
}

func decodePatchInput(input string) (patch, workdir string, valid bool) {
	var payload struct {
		Patch   string `json:"patch"`
		Workdir string `json:"workdir"`
	}
	if json.Unmarshal([]byte(input), &payload) == nil {
		if validPatchEnvelope(payload.Patch) {
			return payload.Patch, payload.Workdir, true
		}
		return "", "", false
	}
	if validPatchEnvelope(input) {
		return input, "", true
	}
	return "", "", false
}

func validPatchEnvelope(patch string) bool {
	lines := normalizedLines(patch)
	return len(lines) >= 3 && lines[0] == "*** Begin Patch" && lines[len(lines)-1] == "*** End Patch"
}

func nativePatchSucceeded(body string) bool {
	lines := normalizedLines(body)
	if len(lines) < 2 || lines[0] != "Success. Updated the following files:" {
		return false
	}
	for _, line := range lines[1:] {
		if len(line) < 3 || !strings.Contains("AMDR", line[:1]) || line[1] != ' ' || strings.TrimSpace(line[2:]) == "" {
			return false
		}
	}
	return true
}

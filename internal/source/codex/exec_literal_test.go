package codex

import (
	"encoding/json"
	"testing"
)

func TestExecWrapperJavaScriptLiteralPreservesCommandEvidence(t *testing.T) {
	for _, input := range []string{
		`const r = await tools.exec_command({cmd:"go test ./...",yield_time_ms:1000,max_output_tokens:2000}); text(r);`,
		`text(await tools.exec_command({cmd:'go test ./...', login:false,}));`,
	} {
		observations, report := decodeToolEnvelopeSession(t, []map[string]any{
			{"type": "custom_tool_call", "call_id": "literal-command", "name": "exec", "input": input},
			{"type": "custom_tool_call_output", "call_id": "literal-command", "output": wrapperOutput("Script completed\nWall time 0.1 seconds\nOutput:", `{"exit_code":0,"output":"ok","wall_time_seconds":0.1}`)},
		})
		v := findToolObservation(observations, "literal-command", "verification")
		if v == nil || v.Outcome != "passed" {
			t.Fatalf("literal command lost verified outcome: %+v; report=%+v", observations, report)
		}
	}
}

func TestExecWrapperLiteralStringEscapesStayExact(t *testing.T) {
	input := `text(await tools.exec_command({cmd:'printf \'hello\'\nnext \\path "x" \u4e2d'}));`
	w, ok := decodeLiteralExecWrapper(input)
	if !ok {
		t.Fatal("literal string rejected")
	}
	var got struct {
		Cmd string `json:"cmd"`
	}
	if err := json.Unmarshal([]byte(w.input), &got); err != nil {
		t.Fatal(err)
	}
	if got.Cmd != "printf 'hello'\nnext \\path \"x\" 中" {
		t.Fatalf("command changed: %q", got.Cmd)
	}
}

func TestExecWrapperLiteralRejectsDynamicOrAmbiguousValues(t *testing.T) {
	for _, body := range []string{
		`{cmd:command}`, `{cmd:'go '+suffix}`, `{cmd:run()}`, `{cmd:'go test',cmd:'git push'}`,
		`{cmd:'go test',"cmd":'git push'}`, `{['cmd']:'go test'}`, `{...options,cmd:'go test'}`,
		"{cmd:`go test ${target}`}", `{cmd:'go test',get cwd(){return '/tmp'}}`,
		`{cmd:'go test',__proto__:{}}`, `{cmd:'go test',yield_time_ms:Infinity}`,
		`{cmd:'bad\q'}`, `{cmd:'bad\uD800'}`, "{cmd:'raw\nline'}",
	} {
		if _, ok := decodeLiteralExecWrapper("text(await tools.exec_command(" + body + "));"); ok {
			t.Errorf("accepted nonliteral/ambiguous input %s", body)
		}
	}
}

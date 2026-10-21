package main

import (
	"encoding/json"
	"testing"
)

// 一个 worker 服务多个 agent：命令由 run 定义里的 runner.command 决定，
// 只有应用没声明时才回落到 -cmd。
func TestCommandForPrefersDefinition(t *testing.T) {
	w := &worker{cfg: config{command: "fallback-cmd"}}

	withCommand := claimRun{
		AgentSlug:  "agent-a",
		Definition: json.RawMessage(`{"name":"A","runner":{"type":"worker","command":"claude -p {input_file}"}}`),
	}
	if got := w.commandFor(withCommand); got != "claude -p {input_file}" {
		t.Fatalf("commandFor = %q, want per-agent command", got)
	}

	withoutCommand := claimRun{
		AgentSlug:  "agent-b",
		Definition: json.RawMessage(`{"name":"B","runner":{"type":"worker"}}`),
	}
	if got := w.commandFor(withoutCommand); got != "fallback-cmd" {
		t.Fatalf("commandFor = %q, want fallback", got)
	}

	noDefinition := claimRun{AgentSlug: "agent-c"}
	if got := w.commandFor(noDefinition); got != "fallback-cmd" {
		t.Fatalf("commandFor = %q, want fallback", got)
	}

	empty := &worker{cfg: config{}}
	if got := empty.commandFor(noDefinition); got != "" {
		t.Fatalf("commandFor = %q, want empty (no command configured)", got)
	}
}

package server

import (
	"strings"

	"github.com/openchat/openchat/server/store/types"
)

func isUserVisibleMessageType(displayType string) bool {
	switch strings.ToLower(strings.TrimSpace(displayType)) {
	case "text", "image", "voice", "file", "video":
		return true
	default:
		return false
	}
}

// isDurableAgentContextDisplayType lists the message kinds the judged
// transcript keeps. Anything else is agent runtime traffic rather than
// conversation.
func isDurableAgentContextDisplayType(displayType string) bool {
	switch strings.ToLower(strings.TrimSpace(displayType)) {
	case "text", "image", "voice", "file":
		return true
	default:
		return false
	}
}

// isJudgeableActivationMessage reports whether a group message may trigger an
// activation judgement.
//
// It deliberately reuses the judged transcript's own predicate. A message the
// transcript drops cannot be judged either: the judge would be asked about
// something it cannot see, and any answer could only come from the model's
// imagination. That is what excludes agent working traffic — tool calls, tool
// results, thinking, runtime plans, stream deltas and task status.
//
// The cost of not excluding it is measured, not theoretical. On a live group
// that ran away, tool traffic was 91% of one bot's messages (342 of 375) and
// every one of them re-ran the judgement, so a bare "execute_shell" reached the
// judge as the latest message and scored 0.56-0.60 for the other bot — enough
// to wake it.
func isJudgeableActivationMessage(msg *ServerMessage) bool {
	if msg == nil || msg.Data == nil {
		return false
	}
	displayType := strings.ToLower(strings.TrimSpace(firstNonEmpty(msg.Data.Type, msg.Data.MsgType)))
	if !isDurableAgentContextDisplayType(displayType) {
		return false
	}
	return !isInternalAgentWorkingMessage(displayType, msg.Data.Content, msg.Data.ContentBlocks)
}

func isInternalAgentWorkingMessage(displayType string, content interface{}, blocks []types.ContentBlock) bool {
	switch strings.ToLower(strings.TrimSpace(displayType)) {
	case "runtime_plan", "thinking", "tool_use", "tool_result", "debug",
		"stream_delta", "stream_cancel", taskStatusType:
		return true
	}

	text := strings.TrimSpace(normalizeContentText(content))
	if strings.HasPrefix(text, "AI文本:") || strings.HasPrefix(text, "AI文本：") {
		return true
	}

	hasInternalBlock := false
	hasUserVisibleBlock := false
	for _, block := range blocks {
		if isInternalAgentContentBlock(block.Type) {
			hasInternalBlock = true
			continue
		}
		switch strings.ToLower(strings.TrimSpace(block.Type)) {
		case "text", "assistant_text", "image", "voice", "file", "video":
			hasUserVisibleBlock = true
		}
	}
	return hasInternalBlock && !hasUserVisibleBlock
}

func isInternalAgentContentBlock(blockType string) bool {
	switch strings.ToLower(strings.TrimSpace(blockType)) {
	case "runtime_plan", "thinking", "tool_use", "tool_result", "debug":
		return true
	default:
		return false
	}
}

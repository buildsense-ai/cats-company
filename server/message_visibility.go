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
		switch strings.ToLower(strings.TrimSpace(block.Type)) {
		case "runtime_plan", "thinking", "tool_use", "tool_result":
			hasInternalBlock = true
		case "text", "assistant_text", "image", "voice", "file", "video":
			hasUserVisibleBlock = true
		}
	}
	return hasInternalBlock && !hasUserVisibleBlock
}

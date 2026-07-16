package server

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/openchat/openchat/server/store/types"
)

type channelOutboundMessage struct {
	Text        string
	Attachments []channelOutboundAttachment
}

type channelOutboundAttachment struct {
	Type     string
	Name     string
	URL      string
	FileKey  string
	Size     int64
	MimeType string
}

func isInternalChannelOutboundPayload(payload *normalizedMessagePayload) bool {
	if payload == nil {
		return false
	}
	switch payload.DisplayType {
	case "runtime_plan", "thinking", "tool_use", "tool_result":
		return true
	case "text", "image", "file":
	default:
		return true
	}
	if strings.HasPrefix(strings.TrimSpace(normalizeContentText(payload.DisplayContent)), "AI文本:") ||
		strings.HasPrefix(strings.TrimSpace(normalizeContentText(payload.DisplayContent)), "AI文本：") {
		return true
	}
	if payload.DisplayType == "text" && len(payload.ContentBlocks) > 0 {
		hasInternalBlock := false
		hasDeliverableBlock := false
		for _, block := range payload.ContentBlocks {
			switch strings.ToLower(strings.TrimSpace(block.Type)) {
			case "thinking", "tool_use", "tool_result":
				hasInternalBlock = true
			case "text", "image", "file":
				hasDeliverableBlock = true
			}
		}
		if hasInternalBlock && !hasDeliverableBlock {
			return true
		}
	}
	return false
}

func channelOutboundTextMessage(text string) channelOutboundMessage {
	return channelOutboundMessage{Text: strings.TrimSpace(text)}
}

func (m channelOutboundMessage) HasContent() bool {
	if strings.TrimSpace(m.Text) != "" {
		return true
	}
	for _, attachment := range m.Attachments {
		if strings.TrimSpace(attachment.Name) != "" || strings.TrimSpace(attachment.URL) != "" || strings.TrimSpace(attachment.FileKey) != "" {
			return true
		}
	}
	return false
}

func (m channelOutboundMessage) TextWithAttachmentLinks() string {
	text := strings.TrimSpace(m.Text)
	if len(m.Attachments) == 0 {
		return text
	}
	lines := make([]string, 0, len(m.Attachments)+2)
	if text != "" {
		lines = append(lines, text)
	}
	if len(m.Attachments) == 1 {
		lines = append(lines, "机器人生成了附件：")
	} else {
		lines = append(lines, fmt.Sprintf("机器人生成了 %d 个附件：", len(m.Attachments)))
	}
	for _, attachment := range m.Attachments {
		label := "[文件]"
		if attachment.Type == "image" {
			label = "[图片]"
		}
		name := strings.TrimSpace(attachment.Name)
		link := channelOutboundAttachmentPublicURL(attachment)
		switch {
		case name != "" && link != "":
			lines = append(lines, fmt.Sprintf("%s %s: %s", label, name, link))
		case name != "":
			lines = append(lines, fmt.Sprintf("%s %s", label, name))
		case link != "":
			lines = append(lines, fmt.Sprintf("%s %s", label, link))
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func channelOutboundMessageFromPayload(payload *normalizedMessagePayload) channelOutboundMessage {
	if payload == nil {
		return channelOutboundMessage{}
	}
	richAttachment, hasRichAttachment := channelOutboundAttachmentFromRichContent(payload.DisplayContent)
	message := channelOutboundMessage{
		Text: strings.TrimSpace(normalizeContentText(payload.DisplayContent)),
	}
	if hasRichAttachment {
		message.Text = ""
	}
	var blockText []string
	seen := map[string]bool{}
	addAttachment := func(attachment channelOutboundAttachment) {
		if strings.TrimSpace(attachment.Type) == "" {
			attachment.Type = "file"
		}
		if attachment.Type != "image" {
			attachment.Type = "file"
		}
		key := strings.Join([]string{attachment.Type, strings.TrimSpace(attachment.URL), strings.TrimSpace(attachment.FileKey), strings.TrimSpace(attachment.Name)}, "\x00")
		if seen[key] {
			return
		}
		seen[key] = true
		message.Attachments = append(message.Attachments, attachment)
	}

	for _, block := range payload.ContentBlocks {
		switch strings.ToLower(strings.TrimSpace(block.Type)) {
		case "text":
			if text := strings.TrimSpace(block.Text); text != "" {
				blockText = append(blockText, text)
			}
		case "image", "file":
			if attachment, ok := channelOutboundAttachmentFromBlock(block); ok {
				addAttachment(attachment)
			}
		}
	}
	if message.Text == "" && len(blockText) > 0 {
		message.Text = strings.Join(blockText, "\n")
	}
	if hasRichAttachment {
		addAttachment(richAttachment)
	}
	return message
}

func channelOutboundAttachmentFromBlock(block types.ContentBlock) (channelOutboundAttachment, bool) {
	kind := strings.ToLower(strings.TrimSpace(block.Type))
	if kind != "image" {
		kind = "file"
	}
	return channelOutboundAttachmentFromPayloadMap(kind, block.Payload)
}

func channelOutboundAttachmentFromRichContent(content interface{}) (channelOutboundAttachment, bool) {
	rich, ok := content.(map[string]interface{})
	if !ok {
		return channelOutboundAttachment{}, false
	}
	kind := strings.ToLower(strings.TrimSpace(channelOutboundMapString(rich, "type")))
	if kind != "image" && kind != "file" {
		return channelOutboundAttachment{}, false
	}
	payload, _ := rich["payload"].(map[string]interface{})
	return channelOutboundAttachmentFromPayloadMap(kind, payload)
}

func channelOutboundAttachmentFromPayloadMap(kind string, payload map[string]interface{}) (channelOutboundAttachment, bool) {
	if payload == nil {
		return channelOutboundAttachment{}, false
	}
	attachment := channelOutboundAttachment{
		Type:     kind,
		Name:     channelOutboundMapString(payload, "name", "file_name", "filename", "title"),
		URL:      channelOutboundMapString(payload, "url", "download_url", "file_url", "image_url"),
		FileKey:  channelOutboundMapString(payload, "file_key", "fileKey"),
		Size:     channelOutboundMapInt64(payload, "size", "file_size", "fileSize"),
		MimeType: channelOutboundMapString(payload, "mime_type", "mimeType", "content_type", "contentType"),
	}
	if attachment.Type != "image" {
		attachment.Type = "file"
	}
	if strings.TrimSpace(attachment.Name) == "" {
		attachment.Name = channelOutboundFileNameFromURL(attachment.URL)
	}
	if strings.TrimSpace(attachment.Name) == "" {
		attachment.Name = strings.TrimSpace(attachment.FileKey)
	}
	if strings.TrimSpace(attachment.URL) == "" && strings.TrimSpace(attachment.FileKey) != "" {
		attachment.URL = channelOutboundUploadURL(attachment.Type, attachment.FileKey)
	}
	if strings.TrimSpace(attachment.Name) == "" && strings.TrimSpace(attachment.URL) == "" && strings.TrimSpace(attachment.FileKey) == "" {
		return channelOutboundAttachment{}, false
	}
	return attachment, true
}

func channelOutboundAttachmentPublicURL(attachment channelOutboundAttachment) string {
	raw := strings.TrimSpace(attachment.URL)
	if raw == "" && strings.TrimSpace(attachment.FileKey) != "" {
		raw = channelOutboundUploadURL(attachment.Type, attachment.FileKey)
	}
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err == nil && parsed.Scheme != "" && parsed.Host != "" {
		return raw
	}
	if strings.HasPrefix(raw, "uploads/") {
		return strings.TrimRight(publicBaseURL(nil), "/") + "/" + raw
	}
	if strings.HasPrefix(raw, "/") {
		return strings.TrimRight(publicBaseURL(nil), "/") + raw
	}
	return raw
}

func channelOutboundUploadURL(kind, fileKey string) string {
	key := strings.TrimSpace(strings.ReplaceAll(fileKey, "\\", "/"))
	if key == "" {
		return ""
	}
	key = strings.TrimLeft(key, "/")
	if strings.HasPrefix(key, "uploads/") {
		return "/" + key
	}
	if strings.HasPrefix(key, "images/") || strings.HasPrefix(key, "files/") {
		return "/uploads/" + key
	}
	subdir := "files"
	if kind == "image" {
		subdir = "images"
	}
	return "/uploads/" + subdir + "/" + key
}

func channelOutboundFileNameFromURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) == 0 {
		return ""
	}
	return strings.TrimSpace(parts[len(parts)-1])
}

func channelOutboundMapString(payload map[string]interface{}, keys ...string) string {
	value, ok := channelOutboundMapLookup(payload, keys...)
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func channelOutboundMapInt64(payload map[string]interface{}, keys ...string) int64 {
	value, ok := channelOutboundMapLookup(payload, keys...)
	if !ok || value == nil {
		return 0
	}
	switch typed := value.(type) {
	case int64:
		return typed
	case int:
		return int64(typed)
	case int32:
		return int64(typed)
	case float64:
		return int64(typed)
	case float32:
		return int64(typed)
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		if err == nil {
			return parsed
		}
	}
	return 0
}

func channelOutboundMapLookup(payload map[string]interface{}, keys ...string) (interface{}, bool) {
	if payload == nil {
		return nil, false
	}
	wanted := map[string]bool{}
	for _, key := range keys {
		wanted[channelOutboundNormalizedKey(key)] = true
	}
	for key, value := range payload {
		if wanted[channelOutboundNormalizedKey(key)] {
			return value, true
		}
	}
	return nil, false
}

func channelOutboundNormalizedKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "_", "")
	value = strings.ReplaceAll(value, "-", "")
	return value
}

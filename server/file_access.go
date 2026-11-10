package server

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// FileAccessService uses indexes to locate messages, never as an authorization grant.
// Each successful read reloads the message, verifies membership and checks current access.
type FileAccessService struct {
	files    store.FileAccessStore
	readable func(int64, *types.Message) bool
	active   func(int64) bool
}

func NewFileAccessService(db store.Store, hub *Hub) *FileAccessService {
	files, _ := db.(store.FileAccessStore)
	if hub == nil {
		hub = &Hub{db: db}
	}
	return &FileAccessService{files: files, active: func(uid int64) bool {
		if db == nil || uid <= 0 {
			return false
		}
		_, status, _ := activeUserByID(uid, db.GetUser)
		return status == 0
	}, readable: func(uid int64, message *types.Message) bool {
		if db == nil || uid <= 0 || message == nil {
			return false
		}
		actor, status, _ := activeUserByID(uid, db.GetUser)
		if status != 0 {
			return false
		}
		if code, _ := hub.validateTopicReadAccess(uid, actor.AccountType, message.TopicID); code != 0 {
			return false
		}
		// Ordinary persisted-message history returns all rows after this same
		// topic read check. Agent-context eligibility is not a read restriction.
		return true
	}}
}

// canonicalFileResource accepts internal keys and canonical URLs only. Absolute upload
// URLs must belong to a configured platform origin, never to an arbitrary caller host.
func canonicalFileResource(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "upload:") {
		raw = "/uploads/" + strings.TrimPrefix(raw, "upload:")
	}
	if strings.HasPrefix(raw, "artifact:") {
		return canonicalArtifactResource(raw)
	}
	if key := canonicalArtifactResource(raw); key != "" {
		return key
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.Opaque != "" || parsed.RawPath != "" {
		return ""
	}
	if parsed.IsAbs() || parsed.Host != "" {
		if parsed.Scheme != "https" || parsed.Host == "" || !filePlatformOriginAllowed(parsed) {
			return ""
		}
	}
	p := parsed.Path
	if !strings.HasPrefix(p, "/uploads/") || strings.ContainsAny(p, "\\%\x00") || path.Clean(p) != p {
		return ""
	}
	parts := strings.Split(strings.TrimPrefix(p, "/uploads/"), "/")
	if len(parts) != 2 || (parts[0] != "files" && parts[0] != "images" && parts[0] != "feedback") || !uploadFileNamePattern.MatchString(parts[1]) {
		return ""
	}
	return "upload:" + strings.Join(parts, "/")
}

func filePlatformOriginAllowed(u *url.URL) bool {
	origins := append([]string{os.Getenv("CATSCO_PUBLIC_BASE_URL")}, strings.Split(os.Getenv("CATSCO_FILE_TRUSTED_ORIGINS"), ",")...)
	for _, raw := range origins {
		base, err := url.Parse(strings.TrimSpace(raw))
		if err == nil && base.Scheme == "https" && base.User == nil && strings.EqualFold(base.Host, u.Host) {
			return true
		}
	}
	return false
}

// MessageFileResources is shared by ingestion, backfill and read-time verification.
// Uploads require attachment payloads; artifact links require configured origins.
func MessageFileResources(message *types.Message) []string {
	if message == nil {
		return nil
	}
	keys := map[string]bool{}
	add := func(raw string) {
		if key := canonicalFileResource(raw); key != "" {
			keys[key] = true
		}
	}
	for _, attachment := range fileAttachmentsFromMessage(message) {
		add(attachment.URL)
		add(attachment.Thumbnail)
		if attachment.FileKey != "" {
			add(channelOutboundUploadURL(attachment.Type, attachment.FileKey))
		}
	}
	// Voice/video payloads use the same upload storage. Inspect each recognized URL
	// field, including aliases, so an unused alias cannot smuggle a private reference.
	payloads := []map[string]interface{}{}
	for _, block := range message.ContentBlocks {
		switch strings.ToLower(strings.TrimSpace(block.Type)) {
		case "file", "image", "voice", "video", "artifact":
			payloads = append(payloads, block.Payload)
		}
	}
	switch strings.ToLower(strings.TrimSpace(message.MsgType)) {
	case "file", "image", "voice", "video", "artifact":
		payloads = append(payloads, legacyFilePayload(message.Content))
	}
	for _, payload := range payloads {
		for _, field := range []string{"url", "download_url", "file_url", "image_url", "thumbnail", "thumbnail_url", "thumbnailUrl", "src", "audio_url", "video_url"} {
			if value, ok := payload[field].(string); ok {
				add(value)
			}
		}
	}
	for _, resource := range messageArtifactURLs(normalizeContentText(decodeStoredContent(message.Content))) {
		keys[resource] = true
	}
	for _, block := range message.ContentBlocks {
		if block.Type == "text" || block.Type == "assistant_text" {
			for _, resource := range messageArtifactURLs(block.Text) {
				keys[resource] = true
			}
		}
	}
	out := make([]string, 0, len(keys))
	for key := range keys {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func messageContainsFile(message *types.Message, resource string) bool {
	for _, key := range MessageFileResources(message) {
		if key == resource {
			return true
		}
	}
	return false
}

func (s *FileAccessService) CanReadResource(ctx context.Context, actorUID int64, resource string, messageID int64) bool {
	key := canonicalFileResource(resource)
	if s == nil || s.files == nil || s.readable == nil || actorUID <= 0 || key == "" {
		return false
	}
	if s.active != nil && !s.active(actorUID) {
		return false
	}
	bindings, err := s.files.ListFileMessageBindings(ctx, key)
	if err != nil {
		return false
	}
	for _, binding := range bindings {
		message, err := s.files.GetFileAccessMessage(ctx, binding.MessageID)
		if err == nil && message != nil && message.TopicID == binding.TopicID && messageContainsFile(message, key) && s.readable(actorUID, message) {
			return true
		}
	}
	// An explicit historical hint repairs an index only after verifying the stored
	// message and its permissions. Request order cannot create ownership.
	if messageID > 0 {
		message, err := s.files.GetFileAccessMessage(ctx, messageID)
		if err == nil && message != nil && messageContainsFile(message, key) && s.readable(actorUID, message) {
			return s.files.BindMessageFiles(ctx, message.ID, message.TopicID, MessageFileResources(message)) == nil
		}
	}
	if len(bindings) != 0 {
		return false
	}
	upload, err := s.files.GetFileUpload(ctx, key)
	return err == nil && upload != nil && !upload.Bound && upload.UploaderUID == actorUID
}

func (s *FileAccessService) CanUseResource(ctx context.Context, actorUID int64, resource string) bool {
	return s.CanReadResource(ctx, actorUID, resource, 0)
}

func (s *FileAccessService) RecordUpload(ctx context.Context, actorUID int64, resource string) error {
	key := canonicalFileResource(resource)
	if s == nil || s.files == nil || actorUID <= 0 || key == "" {
		return errors.New("file ownership unavailable")
	}
	return s.files.RecordFileUpload(ctx, key, actorUID)
}

// FileBackfillResult uses a durable message-id cursor. A failed batch may be retried
// from its input cursor because every message/resource binding is idempotent.
type FileBackfillResult struct {
	AfterID           int64 `json:"after_id"`
	NextAfterID       int64 `json:"next_after_id"`
	Scanned           int   `json:"scanned"`
	MessagesWithFiles int   `json:"messages_with_files"`
	References        int   `json:"references"`
	DryRun            bool  `json:"dry_run"`
	HasMore           bool  `json:"has_more"`
}

func (s *FileAccessService) Backfill(ctx context.Context, afterID int64, limit int, dryRun bool) (FileBackfillResult, error) {
	result := FileBackfillResult{AfterID: afterID, NextAfterID: afterID, DryRun: dryRun}
	if s == nil || s.files == nil {
		return result, errors.New("file access store unavailable")
	}
	if afterID < 0 || limit < 1 || limit > 1000 {
		return result, errors.New("after_id must be nonnegative and limit must be 1..1000")
	}
	messages, err := s.files.ListFileBackfillMessages(ctx, afterID, limit)
	if err != nil {
		return result, err
	}
	for _, message := range messages {
		if message == nil || message.ID <= result.NextAfterID {
			return result, errors.New("invalid backfill cursor order")
		}
		keys := MessageFileResources(message)
		if len(keys) > 0 {
			if !dryRun {
				if err := s.files.BindMessageFiles(ctx, message.ID, message.TopicID, keys); err != nil {
					return result, err
				}
			}
			result.MessagesWithFiles++
			result.References += len(keys)
		}
		result.Scanned++
		result.NextAfterID = message.ID
	}
	result.HasMore = len(messages) == limit
	return result, nil
}

package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

type fileBindingFixture struct {
	*openBindingFixture
	files   *FileOpenBindingHandler
	uploads *UploadHandler
	fileHub *Hub
	source  *types.Message
}

func newFileBindingFixture(t *testing.T, topic string) *fileBindingFixture {
	t.Helper()
	parent := newOpenBindingFixture(t)
	db := &fileAnnotationBrowserStore{parent.db}
	hub := NewHub(db, nil)
	uploads := NewUploadHandler(t.TempDir(), "/uploads")
	messages := NewMessageHandler(db, hub)
	files := NewFileOpenBindingHandler(hub, messages, uploads)
	files.now = func() time.Time { return parent.now }
	auth := JWTAuthMiddlewareWithDB(db)
	parent.mux.HandleFunc("POST /api/files/open-bindings", auth(files.HandleOpenBinding))
	parent.mux.HandleFunc("POST /api/files/annotations", auth(files.HandleAnnotations))
	parent.mux.HandleFunc("DELETE /api/files/open-bindings/{open_ref}", auth(files.HandleRevoke))
	source := &types.Message{ID: 1, TopicID: topic, FromUID: 9, MsgType: "image", ContentBlocks: []types.ContentBlock{{Type: "image", Payload: map[string]interface{}{"name": "diagram.png", "url": "/uploads/images/key.png", "file_key": "key.png", "mime_type": "image/png", "size": 120}}}}
	parent.db.durable = append(parent.db.durable, source)
	return &fileBindingFixture{parent, files, uploads, hub, source}
}

func (f *fileBindingFixture) openFile(t *testing.T) (string, map[string]interface{}) {
	t.Helper()
	result := f.request(t, http.MethodPost, "/api/files/open-bindings", f.token, map[string]interface{}{"topic_id": f.source.TopicID, "message_id": f.source.ID, "attachment_index": 0}, 200)
	return result["open_ref"].(string), result["source"].(map[string]interface{})
}

func fileTestDocument(source map[string]interface{}, kind string, target map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{"contract_version": FileAnnotationsContractV1, "source": source, "annotations": []interface{}{map[string]interface{}{"id": "fa_test", "kind": kind, "body": "请调整这里", "target": target}}}
}

func fileTestSubmit(ref string, document map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{"open_ref": ref, "client_msg_id": "file-comment-1", "file_annotations": document}
}

func fileTestRect() map[string]interface{} {
	return map[string]interface{}{"rect": map[string]interface{}{"x": 0.1, "y": 0.2, "width": 0.3, "height": 0.4}}
}

func TestFileBindingDurabilityAndRetry(t *testing.T) {
	f := newFileBindingFixture(t, "p2p_7_9")
	ref, source := f.openFile(t)
	document := fileTestDocument(source, "image", fileTestRect())
	body := fileTestSubmit(ref, document)
	first := f.request(t, "POST", "/api/files/annotations", f.token, body, 200)
	f.now = f.now.Add(time.Second)
	retry := f.request(t, "POST", "/api/files/annotations", f.token, body, 200)
	if first["seq_id"] != retry["seq_id"] || len(f.db.durable) != 2 {
		t.Fatalf("retry created another message: first=%v retry=%v count=%d", first["seq_id"], retry["seq_id"], len(f.db.durable))
	}
	saved := f.db.durable[1]
	if saved.TopicID != "p2p_7_9" || saved.FromUID != 7 || saved.Metadata[fileAnnotationsMetadataKey] == nil {
		t.Fatalf("wrong durable message: %+v", saved)
	}
	raw, _ := json.Marshal(saved.Metadata)
	if strings.Contains(string(raw), "fob_") || strings.Contains(string(raw), "fingerprint") || strings.Contains(string(raw), "trusted") {
		t.Fatal("ephemeral authority leaked into durable metadata")
	}
	if !strings.Contains(saved.Content, "请调整这里") {
		t.Fatal("server did not render comment text")
	}
	botContext := f.fileHub.fileAnnotationAgentContext(7, 9, saved.TopicID, saved.Metadata)
	if botContext == nil || !strings.Contains(botContext["summary"].(string), "区域") {
		t.Fatal("bot cannot read target")
	}
	modelText := f.fileHub.fileAnnotationModelText(7, 9, saved.TopicID, saved.Metadata)
	if !strings.Contains(modelText, "/uploads/images/key.png") {
		t.Fatal("plain text-only readers lost source reference")
	}
	if f.fileHub.fileAnnotationAgentContext(7, 7, saved.TopicID, saved.Metadata) != nil {
		t.Fatal("human received bot-only context")
	}
}

func TestFileBindingRejectsWrongActorSessionSourceAndRevocation(t *testing.T) {
	for _, scenario := range []string{"actor", "session", "source", "changed", "revoked", "expired", "removed"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFileBindingFixture(t, "p2p_7_9")
			ref, source := f.openFile(t)
			body := fileTestSubmit(ref, fileTestDocument(source, "image", fileTestRect()))
			token := f.token
			switch scenario {
			case "actor":
				token = f.otherActor
			case "session":
				token = f.otherSession
			case "source":
				source["message_id"] = float64(2)
			case "changed":
				f.source.ContentBlocks[0].Payload["name"] = "replacement.png"
			case "revoked":
				f.request(t, "DELETE", "/api/files/open-bindings/"+ref, f.token, nil, 204)
			case "expired":
				f.now = f.now.Add(fileOpenBindingTTL + time.Second)
			case "removed":
				f.db.durable = nil
			}
			f.request(t, "POST", "/api/files/annotations", token, body, 403)
			expected := 1
			if scenario == "removed" {
				expected = 0
			}
			if len(f.db.durable) != expected {
				t.Fatal("rejected binding persisted a message")
			}
		})
	}
}

func TestFileBindingCannotBeForgedThroughOrdinaryMessage(t *testing.T) {
	f := newFileBindingFixture(t, "p2p_7_9")
	_, source := f.openFile(t)
	metadata := map[string]interface{}{fileAnnotationsMetadataKey: fileTestDocument(source, "image", fileTestRect()), "catsco_file_annotations_trusted": true}
	f.request(t, "POST", "/api/messages/send", f.token, map[string]interface{}{"topic_id": "p2p_7_9", "type": "text", "content": "unbound comment", "metadata": metadata, "fileAnnotationsTrusted": true}, 400)
	if len(f.db.durable) != 1 {
		t.Fatal("generic client forged bound metadata")
	}
	// The WebSocket path cannot set the private field either; shared validation
	// also rejects a decoded client document independently of HTTP prechecks.
	_, _, err := f.fileHub.validateFileAnnotationsMetadata(7, "p2p_7_9", metadata, false)
	if err == nil {
		t.Fatal("shared ingress trusted client metadata")
	}
}

func TestFileBindingGroupsUseOrdinaryDelivery(t *testing.T) {
	f := newFileBindingFixture(t, "grp_2")
	f.db.groupMembers["2:7"], f.db.groupMembers["2:9"], f.db.groupMembers["2:11"] = true, true, true
	f.db.groups[2] = &types.Group{ID: 2, AgentIDs: []int64{9, 11}}
	f.db.members[2] = []*types.GroupMember{{UserID: 7}, {UserID: 9, IsBot: true}, {UserID: 11, IsBot: true}}
	ref, source := f.openFile(t)
	f.request(t, "POST", "/api/files/annotations", f.token, fileTestSubmit(ref, fileTestDocument(source, "image", fileTestRect())), 200)
	saved := f.db.durable[1]
	if saved.Metadata["mentions"] != nil || saved.Metadata["mention_all"] != nil {
		t.Fatal("file comments invented mentions")
	}
	for _, block := range saved.ContentBlocks {
		if block.Payload != nil && block.Payload["mentions"] != nil {
			t.Fatal("file comments invented structured mentions")
		}
	}
	for _, uid := range []int64{9, 11} {
		if f.fileHub.fileAnnotationAgentContext(7, uid, "grp_2", saved.Metadata) == nil {
			t.Fatalf("authorized bot %d lost context", uid)
		}
	}
	f.db.groupMuted["2:7"] = true
	f.request(t, "POST", "/api/files/annotations", f.token, fileTestSubmit(ref, fileTestDocument(source, "image", fileTestRect())), 403)
}

func TestFileAnnotationZeroCoordinatesRoundTrip(t *testing.T) {
	for _, kind := range []string{"video", "text"} {
		target := fileTestRect()
		if kind == "video" {
			target["time_seconds"] = 0
		} else {
			target = map[string]interface{}{"start": 0, "end": 4, "quote": "你好😀", "line_start": 1, "line_end": 1}
		}
		source := map[string]interface{}{"topic_id": "p2p_7_9", "message_id": 1, "attachment_index": 0, "name": "file.txt", "url": "/uploads/files/key.txt", "file_key": "key.txt", "type": "file", "mime_type": "text/plain", "size": 12, "version": strings.Repeat("a", 64)}
		document, err := normalizeFileAnnotations(fileTestDocument(source, kind, target))
		if err != nil {
			t.Fatal(err)
		}
		stored, err := document.restamped()
		if err != nil {
			t.Fatal(err)
		}
		annotations := stored["annotations"].([]interface{})
		got := annotations[0].(map[string]interface{})["target"].(map[string]interface{})
		key := "start"
		if kind == "video" {
			key = "time_seconds"
		}
		if value, ok := got[key]; !ok || value != float64(0) {
			t.Fatalf("lost %s=0: %v", key, got)
		}
		if _, err = normalizeFileAnnotations(stored); err != nil {
			t.Fatalf("stored document not replayable: %v", err)
		}
	}
}

func TestFileAnnotationQuoteUsesBrowserUTF16AndBOM(t *testing.T) {
	uploads := NewUploadHandler(t.TempDir(), "/uploads")
	key := "20261009_00000000000000000000000000000001.ts"
	dir := filepath.Join(uploads.baseDir, "files")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, key), []byte("\ufeff你好😀世界\n第二行"), 0600); err != nil {
		t.Fatal(err)
	}
	source := fileAnnotationSource{FileKey: key}
	good := fileAnnotationTarget{Start: 0, End: 4, Quote: "你好😀", LineStart: 1, LineEnd: 1}
	if err := uploads.verifyFileTextQuote(source, good); err != nil {
		t.Fatalf("browser selection failed: %v", err)
	}
	good.Quote = "错误文本"
	if uploads.verifyFileTextQuote(source, good) == nil {
		t.Fatal("mismatched quote was accepted")
	}
}

func TestFileAnnotationReplayRequiresCurrentSource(t *testing.T) {
	f := newFileBindingFixture(t, "p2p_7_9")
	ref, source := f.openFile(t)
	f.request(t, "POST", "/api/files/annotations", f.token, fileTestSubmit(ref, fileTestDocument(source, "image", fileTestRect())), 200)
	saved := f.db.durable[1]
	valid := f.fileHub.historyMessageDataForRecipient(9, saved)
	if valid.Metadata["file_annotations_context"] == nil || !strings.Contains(normalizeContentText(valid.Content), "文件引用：") {
		t.Fatal("valid history lost readable source")
	}
	f.source.ContentBlocks[0].Payload["name"] = "replacement.png"
	stale := f.fileHub.historyMessageDataForRecipient(9, saved)
	if stale.Metadata[fileAnnotationsMetadataKey] != nil || stale.Metadata["file_annotations_context"] != nil || strings.Contains(normalizeContentText(stale.Content), "[文件批注上下文]") {
		t.Fatal("stale source remained authoritative in bot replay")
	}
	human := f.fileHub.historyMessageDataForRecipient(7, saved)
	if human.Metadata[fileAnnotationsMetadataKey] == nil || saved.Metadata[fileAnnotationsMetadataKey] == nil {
		t.Fatal("read filtering mutated human history or persistence")
	}
}

func TestFileBindingRejectsInconsistentFileKey(t *testing.T) {
	f := newFileBindingFixture(t, "p2p_7_9")
	f.source.ContentBlocks[0].Payload["file_key"] = "other.png"
	f.request(t, "POST", "/api/files/open-bindings", f.token, map[string]interface{}{"topic_id": f.source.TopicID, "message_id": 1, "attachment_index": 0}, 404)
}

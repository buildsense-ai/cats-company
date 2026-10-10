package server

// Opt-in local browser verification using real auth/upload/file-binding/message
// handlers and the normal in-memory test store. It never runs in ordinary CI.
import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

type fileAnnotationBrowserStore struct{ *openBindingTestStore }

func (s *fileAnnotationBrowserStore) GetMessagesSince(topic string, since int64, limit int) ([]*types.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]*types.Message, 0)
	for _, message := range s.durable {
		if message.TopicID == topic && message.ID > since {
			result = append(result, message)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func TestFileAnnotationBrowserFixture(t *testing.T) {
	if os.Getenv("CATSCO_FILE_ANNOTATION_BROWSER_FIXTURE") != "1" {
		t.Skip("opt-in local browser fixture")
	}
	root := "/tmp/catsco-file-annotations-verify"
	db := &fileAnnotationBrowserStore{&openBindingTestStore{gatewayAnnotationFakeStore: &gatewayAnnotationFakeStore{
		users:     map[int64]*types.User{7: gatewayAnnotationHuman(7), 9: gatewayAnnotationBot(9)},
		botOwners: map[int64]int64{9: 7}, groupMembers: map[string]bool{}, groupMuted: map[string]bool{}, groups: map[int64]*types.Group{}, members: map[int64][]*types.GroupMember{},
	}}}
	type fixtureFile struct{ Name, MIME, Kind, Folder string }
	files := []fixtureFile{{"diagram.png", "image/png", "image", "images"}, {"demo.mp4", "video/mp4", "file", "files"}, {"report.pdf", "application/pdf", "file", "files"}, {"notes.ts", "text/plain", "file", "files"}, {"data.csv", "text/csv", "file", "files"}, {"report.md", "text/markdown", "file", "files"}, {"report.html", "text/html", "file", "files"}}
	uploads := NewUploadHandler(filepath.Join(root, "upload-store"), "/uploads")
	var fileItems []map[string]interface{}
	for i, file := range files {
		data, err := os.ReadFile(filepath.Join(root, "assets", file.Folder, file.Name))
		if err != nil {
			t.Fatal(err)
		}
		directory := filepath.Join(root, "upload-store", file.Folder)
		if err = os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
		key := fmt.Sprintf("20261009_%032x%s", i+1, filepath.Ext(file.Name))
		if err = os.WriteFile(filepath.Join(directory, key), data, 0600); err != nil {
			t.Fatal(err)
		}
		payload := map[string]interface{}{"name": file.Name, "url": "/uploads/" + file.Folder + "/" + key, "file_key": key, "mime_type": file.MIME, "size": len(data)}
		id := int64(i + 1)
		db.durable = append(db.durable, &types.Message{ID: id, TopicID: "p2p_7_9", FromUID: 9, MsgType: file.Kind, ContentBlocks: []types.ContentBlock{{Type: file.Kind, Payload: payload}}, CreatedAt: time.Now().UTC()})
		item := make(map[string]interface{})
		for k, v := range payload {
			item[k] = v
		}
		item["type"] = file.Kind
		item["annotation_source"] = map[string]interface{}{"topic_id": "p2p_7_9", "message_id": id, "attachment_index": 0}
		fileItems = append(fileItems, item)
	}
	hub := NewHub(db, nil)
	messages := NewMessageHandler(db, hub)
	bindings := NewFileOpenBindingHandler(hub, messages, uploads)
	token, err := GenerateToken(7, "human7", "")
	if err != nil {
		t.Fatal(err)
	}
	agent := &Client{uid: 9, accountType: types.AccountBot, send: make(chan []byte, 256)}
	hub.addClient(agent)
	var events []json.RawMessage
	var eventMu sync.Mutex
	go func() {
		for raw := range agent.send {
			eventMu.Lock()
			events = append(events, append(json.RawMessage(nil), raw...))
			eventMu.Unlock()
		}
	}()
	mux := http.NewServeMux()
	auth := JWTAuthMiddlewareWithDB(db)
	mux.HandleFunc("POST /api/files/open-bindings", auth(bindings.HandleOpenBinding))
	mux.HandleFunc("POST /api/files/annotations", auth(bindings.HandleAnnotations))
	mux.HandleFunc("DELETE /api/files/open-bindings/{open_ref}", auth(bindings.HandleRevoke))
	mux.HandleFunc("POST /api/upload", auth(uploads.HandleUpload))
	mux.HandleFunc("GET /api/messages", AuthMiddlewareWithDB(db)(messages.HandleGetMessages))
	mux.HandleFunc("/uploads/", uploads.HandleServeFile)
	mux.HandleFunc("GET /fixture/session", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]interface{}{"token": token, "files": fileItems})
	})
	mux.HandleFunc("GET /fixture/evidence", func(w http.ResponseWriter, r *http.Request) {
		db.mu.Lock()
		saved := append([]*types.Message(nil), db.durable...)
		db.mu.Unlock()
		eventMu.Lock()
		delivered := append([]json.RawMessage(nil), events...)
		eventMu.Unlock()
		evidence := map[string]interface{}{"messages": saved, "agent_events": delivered}
		data, _ := json.MarshalIndent(evidence, "", "  ")
		_ = os.WriteFile(filepath.Join(root, "evidence.json"), data, 0600)
		writeJSON(w, 200, evidence)
	})
	listener, err := net.Listen("tcp", "127.0.0.1:5195")
	if err != nil {
		t.Fatal(err)
	}
	httpServer := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	t.Cleanup(func() { _ = httpServer.Close() })
	go func() { _ = httpServer.Serve(listener) }()
	_ = os.WriteFile(filepath.Join(root, "backend-ready"), []byte("ready"), 0600)
	t.Log("file annotation verification backend listening on loopback 5195")
	<-time.After(2 * time.Hour)
}

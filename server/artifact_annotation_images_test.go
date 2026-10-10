package server

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

func screenshotJPEG(t *testing.T, width, height int) []byte {
	t.Helper()
	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			canvas.Set(x, y, color.RGBA{R: uint8(x * 7), G: uint8(y * 5), B: 123, A: 255})
		}
	}
	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, canvas, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
func (f *openBindingFixture) uploadScreenshot(t *testing.T, token, role string, width, height int, raw bool) types.ContentBlock {
	t.Helper()
	data := screenshotJPEG(t, width, height)
	var body bytes.Buffer
	var contentType string
	path := "/api/upload?type=image"
	if raw {
		body.Write(data)
		contentType = "image/jpeg"
		path += "&raw=1"
	} else {
		form := multipart.NewWriter(&body)
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", `form-data; name="file"; filename="gateway-`+role+`.jpg"`)
		header.Set("Content-Type", "image/jpeg")
		file, err := form.CreatePart(header)
		if err != nil {
			t.Fatal(err)
		}
		file.Write(data)
		form.Close()
		contentType = form.FormDataContentType()
	}
	request, err := http.NewRequest("POST", f.platform.URL+path, &body)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", contentType)
	if raw {
		request.Header.Set(rawUploadFileNameHeader, "gateway-"+role+".jpg")
		request.Header.Set(rawUploadFileSizeHeader, strings.TrimSpace(strconv.Itoa(len(data))))
	}
	response, err := f.platform.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		content, _ := io.ReadAll(response.Body)
		t.Fatalf("upload: %d %s", response.StatusCode, content)
	}
	var upload uploadPayload
	if err := json.NewDecoder(response.Body).Decode(&upload); err != nil {
		t.Fatal(err)
	}
	return types.ContentBlock{Type: "image", Payload: map[string]interface{}{"url": upload.URL, "file_key": upload.FileKey, "name": upload.Name, "mime_type": upload.MimeType, "size": float64(upload.Size), "width": float64(width), "height": float64(height), "screenshot_role": role}}
}

func TestArtifactScreenshotOwnedUploadTwoImagesPersistDeliveryHistoryAndDedup(t *testing.T) {
	f := newOpenBindingFixture(t)
	binding := f.open(t, "p2p_7_9")
	full := f.uploadScreenshot(t, f.token, "full", 80, 60, false)
	crop := f.uploadScreenshot(t, f.token, "crop", 32, 24, true)
	// Owner receipt survives re-creating the upload handler; no in-memory claim.
	old := f.bindings.uploads
	f.bindings.SetUploadHandler(NewUploadHandler(old.baseDir, old.baseURL))
	payload := boundPayload(binding, "screenshot-message")
	payload["content_blocks"] = []types.ContentBlock{full, crop}
	agent := &Client{uid: 9, accountType: types.AccountBot, send: make(chan []byte, 4)}
	f.hub.addClient(agent)
	first := f.request(t, "POST", "/api/artifacts/annotations", f.token, payload, 200)
	retry := f.request(t, "POST", "/api/artifacts/annotations", f.token, payload, 200)
	if retry["duplicate"] != true || retry["seq_id"] != first["seq_id"] || len(f.db.durable) != 1 || len(agent.send) != 1 {
		t.Fatal("screenshot retry duplicated durable or Agent delivery")
	}
	stored := f.db.durable[0]
	if stored.TopicID != binding.TopicID || len(stored.ContentBlocks) != 2 || stored.ContentBlocks[0].Payload["screenshot_role"] != "full" || stored.ContentBlocks[1].Payload["screenshot_role"] != "crop" {
		t.Fatal("two canonical screenshot blocks not persisted")
	}
	var delivered ServerMessage
	if err := json.Unmarshal(<-agent.send, &delivered); err != nil {
		t.Fatal(err)
	}
	imageCount := 0
	for _, block := range delivered.Data.ContentBlocks {
		if block.Type == "image" {
			imageCount++
		}
	}
	if imageCount != 2 || !strings.Contains(xiaoBaEquivalentUserInput(delivered.Data), "Gateway 标注") {
		t.Fatal("live fanout dropped screenshot images or user text")
	}
	botToken, _ := GenerateToken(9, "agent9", "")
	agentHistory := f.request(t, "GET", "/api/messages?topic_id="+binding.TopicID+"&agent_context=1", botToken, nil, 200)
	humanHistory := f.request(t, "GET", "/api/messages?topic_id="+binding.TopicID, f.token, nil, 200)
	if strings.Count(stringJSON(agentHistory), `"type":"image"`) != 2 || strings.Count(stringJSON(humanHistory), `"type":"image"`) != 2 {
		t.Fatal("history dropped screenshot blocks")
	}
	replay := f.hub.historyMessageDataForRecipient(9, stored)
	if len(replay.ContentBlocks) != 4 {
		t.Fatalf("WS replay content blocks lost screenshot/text: %+v", replay.ContentBlocks)
	}
	// Actual stored bytes served and JPEG-decoded, not merely a URL claim.
	for _, block := range stored.ContentBlocks {
		response, err := f.platform.Client().Get(f.platform.URL + block.Payload["url"].(string))
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != 200 {
			t.Fatal("uploaded screenshot unavailable")
		}
		config, err := jpeg.DecodeConfig(bytes.NewReader(data))
		if err != nil || config.Width != block.Payload["width"] || config.Height != block.Payload["height"] {
			t.Fatal("served bytes differ from canonical image")
		}
	}
	if directory := os.Getenv("CATSCO_SCREENSHOT_EXPORT_DIR"); directory != "" {
		if !strings.HasPrefix(directory, "/tmp/") {
			t.Fatal("screenshot export must be under /tmp")
		}
		os.MkdirAll(filepath.Join(directory, "uploads", "images"), 0700)
		for _, block := range stored.ContentBlocks {
			key := block.Payload["file_key"].(string)
			data, err := os.ReadFile(filepath.Join(f.bindings.uploads.baseDir, "images", key))
			if err != nil {
				t.Fatal(err)
			}
			os.WriteFile(filepath.Join(directory, "uploads", "images", key), data, 0600)
		}
		data, _ := json.MarshalIndent(agentHistory, "", "  ")
		os.WriteFile(filepath.Join(directory, "agent-history.json"), data, 0600)
		data, _ = json.MarshalIndent(map[string]interface{}{"live": delivered.Data, "history": replay}, "", "  ")
		os.WriteFile(filepath.Join(directory, "delivery.json"), data, 0600)
		t.Logf("actual HTTP-handler screenshot/history export: %s", directory)
	}
	if root := os.Getenv("XIAOBA_ROOT"); root != "" {
		fixture := map[string]interface{}{"platform_url": f.platform.URL, "live": delivered.Data, "history": replay, "agent_history": agentHistory}
		data, _ := json.Marshal(fixture)
		path := filepath.Join(t.TempDir(), "actual-images.json")
		os.WriteFile(path, data, 0600)
		output, err := exec.Command("node", "testdata/xiaoba-screenshot-vision-runner.cjs", path, root).CombinedOutput()
		if err != nil {
			t.Fatalf("actual consumer image pipeline: %v\n%s", err, output)
		}
		t.Log(string(output))
	}
}

func TestArtifactScreenshotRejectsForeignForgedMissingCorruptAndBudget(t *testing.T) {
	for _, scenario := range []string{"foreign owner", "foreign URL", "query URL", "receipt missing", "missing image", "same key", "same role", "unknown role", "size mismatch", "dimension mismatch", "over dimension", "corrupt bytes", "symlink", "nonJPEG", "unsupported block", "incomplete JPEG", "budget bytes"} {
		t.Run(scenario, func(t *testing.T) {
			f := newOpenBindingFixture(t)
			binding := f.open(t, "p2p_7_9")
			token := f.token
			if scenario == "foreign owner" {
				token = f.otherActor
			}
			full := f.uploadScreenshot(t, token, "full", 80, 60, false)
			crop := f.uploadScreenshot(t, f.token, "crop", 32, 24, false)
			blocks := []types.ContentBlock{full, crop}
			key := full.Payload["file_key"].(string)
			path := filepath.Join(f.bindings.uploads.baseDir, "images", key)
			switch scenario {
			case "foreign URL":
				full.Payload["url"] = "https://evil.example" + full.Payload["url"].(string)
			case "query URL":
				full.Payload["url"] = full.Payload["url"].(string) + "?x=1"
			case "receipt missing":
				os.Remove(filepath.Join(f.bindings.uploads.artifactReceiptDir(), key+".json"))
			case "missing image":
				blocks = blocks[:1]
			case "same key":
				blocks[1] = full
			case "same role":
				crop.Payload["screenshot_role"] = "full"
			case "unknown role":
				crop.Payload["screenshot_role"] = "secret"
			case "size mismatch":
				full.Payload["size"] = float64(2<<20 + 1)
			case "dimension mismatch":
				full.Payload["width"] = float64(81)
			case "over dimension":
				full.Payload["width"] = float64(2049)
			case "corrupt bytes":
				os.WriteFile(path, bytes.Repeat([]byte("x"), int(full.Payload["size"].(float64))), 0644)
			case "symlink":
				os.Remove(path)
				os.Symlink("/etc/hosts", path)
			case "nonJPEG":
				full.Payload["mime_type"] = "image/png"
			case "incomplete JPEG":
				data, _ := os.ReadFile(path)
				data = data[:len(data)/2]
				os.WriteFile(path, data, 0644)
				if err := f.bindings.uploads.rememberArtifactImageUpload(7, key); err != nil {
					t.Fatal(err)
				}
				full.Payload["size"] = float64(len(data))
			case "budget bytes":
				os.WriteFile(path, make([]byte, artifactScreenshotMaxImageBytes+1), 0644)
				full.Payload["size"] = float64(artifactScreenshotMaxImageBytes + 1)
			case "unsupported block":
				blocks = append(blocks, types.ContentBlock{Type: "file", Payload: map[string]interface{}{"url": "https://evil.example"}})
			}
			payload := boundPayload(binding, "invalid-image")
			payload["content_blocks"] = blocks
			f.request(t, "POST", "/api/artifacts/annotations", f.token, payload, 400)
			if len(f.db.durable) != 0 {
				t.Fatal("invalid screenshot persisted")
			}
		})
	}
}

func stringJSON(value interface{}) string { data, _ := json.Marshal(value); return string(data) }

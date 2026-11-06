package server

import (
	"embed"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	tutorialMaxUpload = 50 << 20
	tutorialTaskLimit = 12
)

//go:embed tutorial_admin.html
var tutorialAdminAssets embed.FS

type TutorialTaskHandler struct {
	baseDir string
	baseURL string
	storage UploadStorage
}

type tutorialTaskPayload struct {
	Tasks []tutorialTask `json:"tasks"`
	Limit int            `json:"limit,omitempty"`
}

type tutorialTask struct {
	ID     string             `json:"id"`
	Title  string             `json:"title"`
	Intro  string             `json:"intro"`
	Files  []tutorialTaskFile `json:"files,omitempty"`
	Prompt string             `json:"prompt"`
}

type tutorialTaskFile struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type rawTutorialTask struct {
	ID          string             `json:"id"`
	Title       string             `json:"title"`
	Intro       string             `json:"intro"`
	Description string             `json:"description"`
	Detail      string             `json:"detail"`
	Files       []tutorialTaskFile `json:"files"`
	MediaName   string             `json:"mediaName"`
	MediaURL    string             `json:"mediaUrl"`
	Prompt      string             `json:"prompt"`
}

var allowedTutorialFileExts = map[string]bool{
	".txt": true, ".md": true, ".csv": true, ".json": true,
	".pdf": true, ".doc": true, ".docx": true,
	".xls": true, ".xlsx": true, ".ppt": true, ".pptx": true,
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true,
	".mp3": true, ".mp4": true, ".wav": true,
	".zip": true, ".rar": true, ".7z": true,
}

func NewTutorialTaskHandler(baseDir, baseURL string) *TutorialTaskHandler {
	_ = os.MkdirAll(filepath.Join(baseDir, "tutorial"), 0755)
	return &TutorialTaskHandler{baseDir: baseDir, baseURL: baseURL, storage: NewLocalUploadStorage(baseDir)}
}

func (h *TutorialTaskHandler) SetUploadStorage(storage UploadStorage) {
	if h != nil && storage != nil {
		h.storage = storage
	}
}

func (h *TutorialTaskHandler) HandleTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteJSONPublic(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	payload, err := h.readTasks()
	if err != nil {
		WriteJSONPublic(w, http.StatusInternalServerError, map[string]string{"error": "read failed"})
		return
	}
	WriteJSONPublic(w, http.StatusOK, payload)
}

func (h *TutorialTaskHandler) HandleAdminPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAccountAdminJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if r.URL.Path != "/local/tutorial-admin" && r.URL.Path != "/local/tutorial-admin/" {
		writeAccountAdminJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if !h.requireLocal(w, r) {
		return
	}
	body, err := tutorialAdminAssets.ReadFile("tutorial_admin.html")
	if err != nil {
		writeAccountAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "admin page unavailable"})
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (h *TutorialTaskHandler) HandleAdminTasks(w http.ResponseWriter, r *http.Request) {
	if !h.requireLocal(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		payload, err := h.readTasks()
		if err != nil {
			writeAccountAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "read failed"})
			return
		}
		writeAccountAdminJSON(w, http.StatusOK, payload)
	case http.MethodPut:
		var raw struct {
			Tasks []rawTutorialTask `json:"tasks"`
			Limit int               `json:"limit"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&raw); err != nil {
			writeAccountAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		payload := normalizeTutorialPayload(raw.Tasks, raw.Limit)
		if err := h.writeTasks(payload); err != nil {
			writeAccountAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "save failed"})
			return
		}
		writeAccountAdminJSON(w, http.StatusOK, payload)
	default:
		writeAccountAdminJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (h *TutorialTaskHandler) HandleAdminUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAccountAdminJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if !h.requireLocal(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, tutorialMaxUpload)
	if err := r.ParseMultipartForm(tutorialMaxUpload); err != nil {
		writeAccountAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "file too large"})
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeAccountAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "no file"})
		return
	}
	defer file.Close()

	ext := strings.ToLower(filepath.Ext(header.Filename))
	if !allowedTutorialFileExts[ext] {
		writeAccountAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "file type not allowed"})
		return
	}
	name := generateFileKey(ext)
	size, _, err := saveUploadReader(r.Context(), h.storage, h.baseDir, "uploads/tutorial/"+name, file, tutorialMaxUpload, normalizedUploadMimeType(ext, header.Header.Get("Content-Type")))
	if err != nil {
		writeAccountAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "upload failed"})
		return
	}
	writeAccountAdminJSON(w, http.StatusOK, map[string]interface{}{
		"file": tutorialTaskFile{Name: header.Filename, URL: h.baseURL + "/tutorial/" + name},
		"size": size,
	})
}

func (h *TutorialTaskHandler) requireLocal(w http.ResponseWriter, r *http.Request) bool {
	if isLocalAdminRequest(r) {
		return true
	}
	writeAccountAdminJSON(w, http.StatusForbidden, map[string]string{"error": "local access only"})
	return false
}

func (h *TutorialTaskHandler) readTasks() (tutorialTaskPayload, error) {
	raw, err := os.ReadFile(h.tasksPath())
	if os.IsNotExist(err) {
		return tutorialTaskPayload{Tasks: []tutorialTask{}, Limit: tutorialTaskLimit}, nil
	}
	if err != nil {
		return tutorialTaskPayload{}, err
	}

	var payload struct {
		Tasks []rawTutorialTask `json:"tasks"`
		Limit int               `json:"limit"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return tutorialTaskPayload{}, err
	}
	return normalizeTutorialPayload(payload.Tasks, payload.Limit), nil
}

func (h *TutorialTaskHandler) writeTasks(payload tutorialTaskPayload) error {
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	path := h.tasksPath()
	_ = os.MkdirAll(filepath.Dir(path), 0755)
	return os.WriteFile(path, raw, 0644)
}

func (h *TutorialTaskHandler) tasksPath() string {
	return filepath.Join(h.baseDir, "tutorial", "tasks.json")
}

func normalizeTutorialPayload(rawTasks []rawTutorialTask, limit int) tutorialTaskPayload {
	if limit <= 0 || limit > tutorialTaskLimit {
		limit = tutorialTaskLimit
	}
	tasks := make([]tutorialTask, 0, len(rawTasks))
	for i, raw := range rawTasks {
		task := normalizeTutorialTask(raw, i)
		if task == nil {
			continue
		}
		tasks = append(tasks, *task)
		if len(tasks) >= limit {
			break
		}
	}
	return tutorialTaskPayload{Tasks: tasks, Limit: limit}
}

func normalizeTutorialTask(raw rawTutorialTask, index int) *tutorialTask {
	title := strings.TrimSpace(raw.Title)
	prompt := strings.TrimSpace(raw.Prompt)
	if title == "" || prompt == "" {
		return nil
	}
	intro := strings.TrimSpace(raw.Intro)
	if intro == "" {
		intro = strings.TrimSpace(raw.Detail)
	}
	if intro == "" {
		intro = strings.TrimSpace(raw.Description)
	}

	files := normalizeTutorialTaskFiles(raw)
	id := strings.TrimSpace(raw.ID)
	if id == "" {
		id = "tutorial-task-" + strconv.Itoa(index+1)
	}
	return &tutorialTask{
		ID:     id,
		Title:  title,
		Intro:  intro,
		Files:  files,
		Prompt: prompt,
	}
}

func normalizeTutorialTaskFiles(raw rawTutorialTask) []tutorialTaskFile {
	files := make([]tutorialTaskFile, 0, len(raw.Files)+1)
	for _, file := range raw.Files {
		name := strings.TrimSpace(file.Name)
		url := strings.TrimSpace(file.URL)
		if name == "" || url == "" {
			continue
		}
		files = append(files, tutorialTaskFile{Name: name, URL: url})
	}
	if len(files) == 0 {
		name := strings.TrimSpace(raw.MediaName)
		url := strings.TrimSpace(raw.MediaURL)
		if name != "" && url != "" {
			files = append(files, tutorialTaskFile{Name: name, URL: url})
		}
	}
	return files
}

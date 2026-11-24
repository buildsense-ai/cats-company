package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/openchat/openchat/server/db/wikiinjection"
	"github.com/openchat/openchat/server/store/types"
)

const wikiInjectionType = "wiki_injection"

var wikiInjectionEventID = regexp.MustCompile("^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}:[1-9][0-9]{0,15}$")

type wikiInjectionReference struct {
	WikiID       string `json:"wiki_id"`
	Evidence     string `json:"evidence_level"`
	Contribution string `json:"contribution"`
	Title        string `json:"title,omitempty"`
}

type wikiInjectionMarker struct {
	Type      string    `json:"type"`
	EventID   string    `json:"event_id"`
	Timestamp time.Time `json:"timestamp"`
	Kind      string    `json:"kind"`
}

type wikiInjectionDetail struct {
	wikiInjectionMarker
	Summary    string                   `json:"summary,omitempty"`
	References []wikiInjectionReference `json:"references"`
}

func (h *WikiMemoryHandler) injectionStorage() *wikiinjection.Storage {
	provider, ok := h.db.(interface{ WikiInjections() *wikiinjection.Storage })
	if !ok {
		return nil
	}
	return provider.WikiInjections()
}

func (h *WikiMemoryHandler) HandlePublishInjection(w http.ResponseWriter, r *http.Request) {
	storage := h.injectionStorage()
	if storage == nil || h.hub == nil || !h.configured() {
		wikiMemoryError(w, http.StatusServiceUnavailable)
		return
	}
	var body struct {
		wikiInjectionDetail
		TopicID string `json:"topic_id"`
		ReplyTo int    `json:"reply_to"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&body) != nil ||
		!wikiInjectionEventID.MatchString(body.EventID) || body.Timestamp.IsZero() ||
		!slices.Contains([]string{"previous", "initial", "refresh"}, body.Kind) ||
		body.Summary == "" || len(body.References) == 0 {
		wikiMemoryError(w, http.StatusBadRequest)
		return
	}
	seen := make(map[string]bool)
	for i, ref := range body.References {
		if !wikiMemoryID.MatchString(ref.WikiID) || seen[ref.WikiID] ||
			!slices.Contains([]string{"wiki_body", "search_snippet"}, ref.Evidence) {
			wikiMemoryError(w, http.StatusBadRequest)
			return
		}
		seen[ref.WikiID] = true
		body.References[i].Title = ""
	}
	bot := UIDFromContext(r.Context())
	body.TopicID = strings.TrimSpace(body.TopicID)
	if code, _ := h.hub.validateMessagePublish(bot, types.AccountBot, body.TopicID, false); code != 0 {
		wikiMemoryError(w, code)
		return
	}
	body.Type = wikiInjectionType
	detail, err := json.Marshal(body.wikiInjectionDetail)
	if err == nil {
		err = storage.Save(r.Context(), bot, body.EventID, body.TopicID, detail)
	}
	if err != nil {
		wikiMemoryError(w, http.StatusServiceUnavailable)
		return
	}
	topic, stored, err := storage.Load(r.Context(), bot, body.EventID)
	if err != nil {
		wikiMemoryError(w, http.StatusServiceUnavailable)
		return
	}
	if topic != body.TopicID {
		wikiMemoryError(w, http.StatusConflict)
		return
	}
	var marker wikiInjectionMarker
	if json.Unmarshal(stored, &marker) != nil {
		wikiMemoryError(w, http.StatusServiceUnavailable)
		return
	}
	content, _ := json.Marshal(marker)
	NewMessageHandler(h.db, h.hub).sendMessage(w, r, SendMessageRequest{
		TopicID: topic, Type: wikiInjectionType, Content: content, ReplyTo: body.ReplyTo,
		ClientMsgID: "wiki:" + body.EventID, wikiInjectionTrusted: true,
	}, nil)
}

func (h *WikiMemoryHandler) HandleInjection(w http.ResponseWriter, r *http.Request) {
	viewer, bot, ok := h.identify(w, r)
	if !ok {
		return
	}
	id := r.PathValue("event")
	if !wikiInjectionEventID.MatchString(id) || h.hub == nil {
		wikiMemoryError(w, http.StatusNotFound)
		return
	}
	storage := h.injectionStorage()
	if storage == nil {
		wikiMemoryError(w, http.StatusServiceUnavailable)
		return
	}
	topic, raw, err := storage.Load(r.Context(), bot, id)
	if err != nil {
		wikiMemoryError(w, http.StatusServiceUnavailable)
		return
	}
	if len(raw) == 0 {
		wikiMemoryError(w, http.StatusNotFound)
		return
	}
	if code, _ := h.hub.validateTopicReadAccess(viewer, types.AccountHuman, topic); code != 0 {
		if code < 500 {
			code = http.StatusNotFound
		}
		wikiMemoryError(w, code)
		return
	}
	var detail wikiInjectionDetail
	if json.Unmarshal(raw, &detail) != nil {
		wikiMemoryError(w, http.StatusServiceUnavailable)
		return
	}
	visible := make([]wikiInjectionReference, 0, len(detail.References))
	for _, ref := range detail.References {
		entry, found, err := h.catalog.GetWikiCatalogEntry(r.Context(), ref.WikiID)
		if err != nil {
			wikiMemoryError(w, http.StatusServiceUnavailable)
			return
		}
		if !found || entry.BotUID != strconv.FormatInt(bot, 10) {
			continue
		}
		status := h.documentAccess(viewer, bot, entry.TopicID)
		if status >= 500 {
			wikiMemoryError(w, status)
			return
		}
		if status == http.StatusOK {
			ref.Title = entry.Title
			visible = append(visible, ref)
		}
	}
	restricted := len(visible) != len(detail.References)
	if restricted {
		detail.Summary = ""
		for i := range visible {
			visible[i].Contribution = ""
		}
	}
	detail.References = visible
	if r.Method == http.MethodPut {
		var vote struct {
			WikiID string `json:"wiki_id"`
			Value  *int   `json:"vote"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048)).Decode(&vote) != nil ||
			vote.Value == nil || *vote.Value < -1 || *vote.Value > 1 {
			wikiMemoryError(w, http.StatusBadRequest)
			return
		}
		allowed := len(visible) > 0 && vote.WikiID == ""
		for _, ref := range visible {
			allowed = allowed || ref.WikiID == vote.WikiID
		}
		if !allowed {
			wikiMemoryError(w, http.StatusNotFound)
			return
		}
		if err := storage.SetVote(r.Context(), bot, id, viewer, vote.WikiID, *vote.Value); err != nil {
			wikiMemoryError(w, http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	votes, err := storage.ViewerVotes(r.Context(), bot, id, viewer)
	if err != nil {
		wikiMemoryError(w, http.StatusServiceUnavailable)
		return
	}
	result := struct {
		wikiInjectionDetail
		Restricted bool           `json:"restricted"`
		Votes      map[string]int `json:"votes"`
	}{detail, restricted, make(map[string]int)}
	if len(visible) > 0 {
		result.Votes[""] = votes[""]
	}
	for _, ref := range visible {
		result.Votes[ref.WikiID] = votes[ref.WikiID]
	}
	writeWikiMemoryJSON(w, http.StatusOK, result)
}

func (h *WikiMemoryHandler) HandleInjectionFeedback(w http.ResponseWriter, r *http.Request) {
	expected := sha256.Sum256([]byte("Bearer " + h.token))
	actual := sha256.Sum256([]byte(r.Header.Get("Authorization")))
	if h.token == "" || subtle.ConstantTimeCompare(actual[:], expected[:]) != 1 {
		wikiMemoryError(w, http.StatusUnauthorized)
		return
	}
	var body struct {
		BotUID   string   `json:"bot_uid"`
		EventIDs []string `json:"event_ids"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&body) != nil {
		wikiMemoryError(w, http.StatusBadRequest)
		return
	}
	bot, err := strconv.ParseInt(body.BotUID, 10, 64)
	if err != nil || bot <= 0 || len(body.EventIDs) == 0 || len(body.EventIDs) > 100 {
		wikiMemoryError(w, http.StatusBadRequest)
		return
	}
	for _, id := range body.EventIDs {
		if !wikiInjectionEventID.MatchString(id) {
			wikiMemoryError(w, http.StatusBadRequest)
			return
		}
	}
	storage := h.injectionStorage()
	if storage == nil {
		wikiMemoryError(w, http.StatusServiceUnavailable)
		return
	}
	votes, err := storage.Totals(r.Context(), bot, body.EventIDs)
	if err != nil {
		wikiMemoryError(w, http.StatusServiceUnavailable)
		return
	}
	writeWikiMemoryJSON(w, http.StatusOK, map[string]any{"votes": votes})
}

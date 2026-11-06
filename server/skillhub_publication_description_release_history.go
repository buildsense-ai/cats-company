package server

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"time"

	"github.com/openchat/openchat/server/store"
)

// A browser-safe, explicit projection of local terminal evidence only. Do not
// serialize the receipt, target, derivation or publication observation itself.
type publicationDescriptionReleaseHistoryView struct {
	Stage      string                                          `json:"stage"`
	RecordedAt *time.Time                                      `json:"recordedAt,omitempty"`
	Result     *publicationDescriptionReleaseHistoryResultView `json:"result,omitempty"`
	Code       string                                          `json:"code,omitempty"`
}

type publicationDescriptionReleaseHistoryResultView struct {
	SkillID     string `json:"skillId"`
	Version     string `json:"version"`
	PublishedAt string `json:"publishedAt"`
}

// Even alternate adapters must supply valid, actor-bound history. Bracket the
// receipt read with original-publisher checks and freeze every pointer before
// the second adapter call. No Cloud read/write or live Bot dependency belongs
// here; lack of a receipt is NOT evidence of an in-progress/failed release.
func readConfirmedPublicationDescriptionReleaseTerminal(ctx context.Context, db store.SkillHubPublicationDescriptionReleaseTerminalReader,
	actor int64, id string,
) (*store.SkillHubPublicationDescriptionReleaseTerminalReceipt, error) {
	if db == nil || actor <= 0 || !store.ValidSkillHubPublicationDeliveryEntryID(id) {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	o, err := db.GetSkillHubPublicationDescriptionReleaseObservation(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	if o == nil || o.ValidateFor(actor, id) != nil {
		return nil, store.ErrSkillHubPublicationConflict
	}
	baseline := snapshotDescriptionReleaseObservation(*o)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r, err := db.GetSkillHubPublicationDescriptionReleaseTerminal(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var frozen *store.SkillHubPublicationDescriptionReleaseTerminalReceipt
	if r != nil {
		copy := *r
		if r.Terminal.Result != nil {
			p := *r.Terminal.Result
			copy.Terminal.Result = &p
		}
		if copy.ValidateFor(baseline) != nil {
			return nil, store.ErrSkillHubPublicationConflict
		}
		frozen = &copy
	}
	current, err := db.GetSkillHubPublicationDescriptionReleaseObservation(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	if current == nil || current.ValidateFor(actor, id) != nil || !reflect.DeepEqual(*current, baseline) {
		return nil, store.ErrSkillHubPublicationConflict
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return frozen, nil
}

func (h *SkillHubPublicationReadHandler) readDescriptionReleaseHistory(w http.ResponseWriter, ctx context.Context, actor int64, id string) {
	reader, ok := h.db.(store.SkillHubPublicationDescriptionReleaseTerminalReader)
	if !ok {
		skillHubPublicationReadError(w, http.StatusServiceUnavailable, "publication.storage_unsupported")
		return
	}
	r, err := readConfirmedPublicationDescriptionReleaseTerminal(ctx, reader, actor, id)
	if errors.Is(err, store.ErrSkillHubPublicationDenied) || errors.Is(err, store.ErrSkillHubPublicationInvalid) {
		skillHubPublicationReadError(w, http.StatusNotFound, "publication.not_found")
		return
	}
	if err != nil || ctx.Err() != nil {
		skillHubPublicationReadError(w, http.StatusServiceUnavailable, "publication.read_unavailable")
		return
	}
	v := publicationDescriptionReleaseHistoryView{Stage: "not_recorded"}
	if r != nil {
		at := r.RecordedAt.UTC()
		v.Stage, v.RecordedAt = r.Terminal.State, &at
		if p := r.Terminal.Result; p != nil {
			v.Result = &publicationDescriptionReleaseHistoryResultView{SkillID: p.SkillID, Version: p.Version, PublishedAt: p.PublishedAt}
		}
		v.Code = r.Terminal.ErrorCode // Validated static allowlist, never a raw error.
	}
	writeSkillHubPublicationJSON(w, http.StatusOK, map[string]any{
		"schemaVersion": 1, "entryRequestId": id, "descriptionReleaseHistory": v,
	})
}

package server

import (
	"encoding/json"
	"testing"
)

func TestSkillHubPublicationDescriptionUICapability(t *testing.T) {
	flags := []string{
		"CATSCO_SKILLHUB_PUBLICATION_READS_ENABLED",
		"CATSCO_SKILLHUB_PUBLICATION_HUMAN_COMMIT_ENABLED",
		"CATSCO_SKILLHUB_PUBLICATION_REPAIR_ADMISSION_ENABLED",
		"CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_CONFIRMATION_UI_ENABLED",
	}
	for mask := 0; mask < 16; mask++ {
		for i, flag := range flags {
			value := "false"
			if mask&(1<<i) != 0 {
				value = "TRUE"
			}
			t.Setenv(flag, value)
		}
		db, _, _ := descriptionFixture(t)
		h := NewSkillHubPublicationReadHandlerFromEnv(db)
		w := runPublicationRead(t, h, publicationReadRequest(t, "GET", skillHubPublicationPrefix+"/capabilities", 1), 200)
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out["descriptionConfirmationUiEnabled"] != (mask == 15) {
			t.Fatalf("wrong gated capability mask=%d body=%s err=%v", mask, w.Body.String(), err)
		}
		if db.reads != 0 || db.confirmations != 0 || db.proposals != 0 {
			t.Fatal("capability discovery accessed drafts")
		}
	}
	// All flags on cannot compensate for a missing draft/repair adapter.
	db := newPublicationReadTestStore()
	h := NewSkillHubPublicationReadHandlerFromEnv(db)
	w := runPublicationRead(t, h, publicationReadRequest(t, "GET", skillHubPublicationPrefix+"/capabilities", 1), 200)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out["descriptionConfirmationUiEnabled"] != false {
		t.Fatal("missing adapter advertised confirmation")
	}
}

package server

import (
	"encoding/json"
	"testing"
)

func TestSkillHubPublicationRepairAdmissionUICapability(t *testing.T) {
	flags := []string{
		"CATSCO_SKILLHUB_PUBLICATION_READS_ENABLED",
		"CATSCO_SKILLHUB_PUBLICATION_HUMAN_COMMIT_ENABLED",
		"CATSCO_SKILLHUB_PUBLICATION_REPAIR_ADMISSION_ENABLED",
		"CATSCO_SKILLHUB_PUBLICATION_REPAIR_ADMISSION_UI_ENABLED",
	}
	for mask := 0; mask < 16; mask++ {
		for i, flag := range flags {
			value := "false"
			if mask&(1<<i) != 0 {
				value = "1"
			}
			t.Setenv(flag, value)
		}
		db, _, _ := descriptionFixture(t)
		h := NewSkillHubPublicationReadHandlerFromEnv(db)
		w := runPublicationRead(t, h, publicationReadRequest(t, "GET", skillHubPublicationPrefix+"/capabilities", 1), 200)
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out["repairTaskAdmissionUiEnabled"] != (mask == 15) {
			t.Fatalf("wrong admission UI capability mask=%d body=%s err=%v", mask, w.Body.String(), err)
		}
		if db.starts != 1 || db.reads != 0 || db.repairAdmissionDB.reads != 0 || db.confirmations != 0 || db.proposals != 0 || db.records != 0 {
			t.Fatal("discovery accessed repair/draft/commit state")
		}
	}
	for _, value := range []string{"", "false", "yes", "TRUE", "true"} {
		t.Setenv(flags[3], value)
		db, _, _ := descriptionFixture(t)
		h := NewSkillHubPublicationReadHandlerFromEnv(db)
		if h.repairAdmissionUIEnabled != (value == "TRUE" || value == "true") {
			t.Fatal("invalid boolean accepted", value)
		}
	}
	for _, flag := range flags {
		t.Setenv(flag, "true")
	}
	h := NewSkillHubPublicationReadHandlerFromEnv(newPublicationReadTestStore())
	if h.repairAdmissionUIEnabled {
		t.Fatal("missing human repair adapters advertised support")
	}
}

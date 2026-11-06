package server

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

type publicationDescriptionReleaseFlowStore struct {
	*descriptionContinuationTestStore
	intent      *store.SkillHubPublicationDescriptionReleaseIntent
	intentCalls int
}

func (db *publicationDescriptionReleaseFlowStore) GetSkillHubPublicationDescriptionReleaseIntent(_ context.Context, actor int64, id string) (*store.SkillHubPublicationDescriptionReleaseIntent, error) {
	if db.intent == nil {
		return nil, nil
	}
	if actor != db.intent.ActorUID || id != db.intent.EntryID {
		return nil, store.ErrSkillHubPublicationDenied
	}
	db.intentCalls++
	copy := *db.intent
	return &copy, nil
}

func TestAuthorizeConfirmedPublicationDescriptionReleaseFlow(t *testing.T) {
	for _, confirmVersion := range []bool{false, true} {
		t.Run(fmt.Sprintf("confirmVersion=%t", confirmVersion), func(t *testing.T) {
			db, continuationPorts, _ := descriptionContinuationFixture(t)
			flowDB := &publicationDescriptionReleaseFlowStore{descriptionContinuationTestStore: db}
			checkpoint, err := continueConfirmedPublicationDescription(context.Background(), flowDB, 1, db.app.Input.Receipt.Query.EntryID, continuationPorts)
			if err != nil {
				t.Fatal(err)
			}
			plan := descriptionPlanFixture(checkpoint.checkpoint.Application, confirmVersion)
			flowDB.intent = releaseFlowIntent(checkpoint.checkpoint, plan.Target)
			var proof publicationDescriptionCloudReleasePermit
			ports := publicationDescriptionReleasePorts{publicationDescriptionContinuationPorts: continuationPorts,
				PlanRetainedCopy: func(_ context.Context, app store.SkillHubPublicationDescriptionApplication, confirm bool) (*publicationDescriptionReleasePlan, error) {
					if confirm != confirmVersion {
						t.Fatalf("version policy changed: got %t want %t", confirm, confirmVersion)
					}
					copy := descriptionPlanFixture(app, confirm)
					return &copy, nil
				},
				AuthorizeCloud: func(_ context.Context, app store.SkillHubPublicationDescriptionApplication, intent store.SkillHubPublicationDescriptionReleaseIntent) (*publicationDescriptionCloudReleasePermit, error) {
					if flowDB.intent == nil || app.ID != flowDB.intent.ApplicationID || intent != *flowDB.intent {
						t.Fatal("cloud authorization was not bound to the pre-recorded human intent")
					}
					proof = publicationDescriptionCloudReleasePermit{SchemaVersion: 1, ApplicationID: intent.ApplicationID,
						EntryID: intent.EntryID, Target: intent.Target, IntentHash: intent.IdentityHash,
						PermitID:     "pubdescpermit_" + strings.Repeat("a", 32),
						AuthorizedAt: intent.RequestedAt.Add(time.Second).Format(time.RFC3339Nano)}
					replayed := false
					proof.Replayed = &replayed
					return &proof, nil
				},
			}
			got, err := authorizeConfirmedPublicationDescriptionReleaseFlow(context.Background(), flowDB, 1,
				db.app.Input.Receipt.Query.EntryID, ports)
			if err != nil || got == nil || !reflect.DeepEqual(got.permit, proof) || got.plan.Target.ConfirmVersionPublish != confirmVersion || flowDB.intentCalls == 0 || flowDB.task.State() != "waiting_confirmation" || len(flowDB.task.Rounds) != 1 {
				t.Fatalf("release flow failed: err=%v got=%+v intentCalls=%d", err, got, flowDB.intentCalls)
			}
			if flowDB.completes != 1 || flowDB.writes != 1 {
				t.Fatal("release flow repeated derivation or changed repair history")
			}
			// A replay must reuse the durable intent and Cloud permit target, not
			// create a second candidate or alter the original waiting round.
			replayed, err := authorizeConfirmedPublicationDescriptionReleaseFlow(context.Background(), flowDB, 1,
				db.app.Input.Receipt.Query.EntryID, ports)
			if err != nil || replayed == nil || !reflect.DeepEqual(replayed.permit, got.permit) || flowDB.intentCalls < 2 {
				t.Fatalf("release replay changed handoff: err=%v got=%+v", err, replayed)
			}
		})
	}
}

func TestAdmitConfirmedPublicationDescriptionReleaseFlowRequiresDurableIntentAndPermit(t *testing.T) {
	db, continuationPorts, _ := descriptionContinuationFixture(t)
	flowDB := &publicationDescriptionReleaseFlowStore{descriptionContinuationTestStore: db}
	checkpoint, err := continueConfirmedPublicationDescription(context.Background(), flowDB, 1, db.app.Input.Receipt.Query.EntryID, continuationPorts)
	if err != nil {
		t.Fatal(err)
	}
	plan := descriptionPlanFixture(checkpoint.checkpoint.Application, false)
	flowDB.intent = releaseFlowIntent(checkpoint.checkpoint, plan.Target)
	permit := publicationDescriptionCloudReleasePermit{SchemaVersion: 1, ApplicationID: flowDB.intent.ApplicationID,
		EntryID: flowDB.intent.EntryID, Target: flowDB.intent.Target, IntentHash: flowDB.intent.IdentityHash,
		PermitID: "pubdescpermit_" + strings.Repeat("b", 32), AuthorizedAt: flowDB.intent.RequestedAt.Add(time.Second).Format(time.RFC3339Nano)}
	replayed := false
	permit.Replayed = &replayed
	admission := &publicationDescriptionCloudReleaseAdmission{SchemaVersion: 1, ApplicationID: flowDB.intent.ApplicationID,
		TaskID: db.app.Input.Receipt.TaskID, PermitID: permit.PermitID, State: "queued", Replayed: &replayed}
	authorized, admitted := 0, 0
	ports := publicationDescriptionReleasePorts{publicationDescriptionContinuationPorts: continuationPorts,
		PlanRetainedCopy: func(_ context.Context, app store.SkillHubPublicationDescriptionApplication, confirm bool) (*publicationDescriptionReleasePlan, error) {
			p := descriptionPlanFixture(app, confirm)
			return &p, nil
		},
		AuthorizeCloud: func(_ context.Context, _ store.SkillHubPublicationDescriptionApplication, got store.SkillHubPublicationDescriptionReleaseIntent) (*publicationDescriptionCloudReleasePermit, error) {
			authorized++
			if got != *flowDB.intent {
				t.Fatal("permit request was not bound to the durable intent")
			}
			return &permit, nil
		},
		AdmitCloud: func(_ context.Context, app store.SkillHubPublicationDescriptionApplication, got store.SkillHubPublicationDescriptionReleaseIntent, gotPermit publicationDescriptionCloudReleasePermit) (*publicationDescriptionCloudReleaseAdmission, error) {
			admitted++
			if app.ID != got.ApplicationID || got != *flowDB.intent || !reflect.DeepEqual(gotPermit, permit) {
				t.Fatal("Cloud admission was not bound to the verified intent and permit")
			}
			return admission, nil
		},
	}
	got, err := admitConfirmedPublicationDescriptionReleaseFlow(context.Background(), flowDB, 1, db.app.Input.Receipt.Query.EntryID, ports)
	if err != nil || got == nil || got.admission.State != "queued" || authorized != 1 || admitted != 1 {
		t.Fatalf("derived release admission failed: got=%+v err=%v authorize=%d admit=%d", got, err, authorized, admitted)
	}
	replayed = true
	if got.admission.Replayed == nil || *got.admission.Replayed {
		t.Fatal("returned admission changed after the Cloud adapter mutated its response")
	}
}

func TestAdmitConfirmedPublicationDescriptionReleaseFlowRejectsInvalidAdmission(t *testing.T) {
	cases := []struct {
		name        string
		nilResponse bool
		mutate      func(*publicationDescriptionCloudReleaseAdmission)
	}{
		{"nil-response", true, nil},
		{"empty-response", false, func(got *publicationDescriptionCloudReleaseAdmission) {
			*got = publicationDescriptionCloudReleaseAdmission{}
		}},
		{"schema", false, func(got *publicationDescriptionCloudReleaseAdmission) { got.SchemaVersion++ }},
		{"application", false, func(got *publicationDescriptionCloudReleaseAdmission) { got.ApplicationID = "other" }},
		{"task", false, func(got *publicationDescriptionCloudReleaseAdmission) { got.TaskID = "other" }},
		{"permit", false, func(got *publicationDescriptionCloudReleaseAdmission) { got.PermitID = "other" }},
		{"replayed", false, func(got *publicationDescriptionCloudReleaseAdmission) { got.Replayed = nil }},
		{"state", false, func(got *publicationDescriptionCloudReleaseAdmission) { got.State = "legacy_commit" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, continuationPorts, _ := descriptionContinuationFixture(t)
			flowDB := &publicationDescriptionReleaseFlowStore{descriptionContinuationTestStore: db}
			checkpoint, err := continueConfirmedPublicationDescription(context.Background(), flowDB, 1, db.app.Input.Receipt.Query.EntryID, continuationPorts)
			if err != nil {
				t.Fatal(err)
			}
			plan := descriptionPlanFixture(checkpoint.checkpoint.Application, false)
			flowDB.intent = releaseFlowIntent(checkpoint.checkpoint, plan.Target)
			permit := publicationDescriptionCloudReleasePermit{SchemaVersion: 1, ApplicationID: flowDB.intent.ApplicationID,
				EntryID: flowDB.intent.EntryID, Target: flowDB.intent.Target, IntentHash: flowDB.intent.IdentityHash,
				PermitID: "pubdescpermit_" + strings.Repeat("d", 32), AuthorizedAt: flowDB.intent.RequestedAt.Add(time.Second).Format(time.RFC3339Nano)}
			permitReplayed := false
			permit.Replayed = &permitReplayed
			admission := &publicationDescriptionCloudReleaseAdmission{SchemaVersion: 1, ApplicationID: flowDB.intent.ApplicationID,
				TaskID: db.app.Input.Receipt.TaskID, PermitID: permit.PermitID, State: "queued"}
			admissionReplayed := false
			admission.Replayed = &admissionReplayed
			if tc.mutate != nil {
				tc.mutate(admission)
			}
			admitCalls := 0
			ports := publicationDescriptionReleasePorts{publicationDescriptionContinuationPorts: continuationPorts,
				PlanRetainedCopy: func(_ context.Context, app store.SkillHubPublicationDescriptionApplication, confirm bool) (*publicationDescriptionReleasePlan, error) {
					p := descriptionPlanFixture(app, confirm)
					return &p, nil
				},
				AuthorizeCloud: func(context.Context, store.SkillHubPublicationDescriptionApplication, store.SkillHubPublicationDescriptionReleaseIntent) (*publicationDescriptionCloudReleasePermit, error) {
					return &permit, nil
				},
				AdmitCloud: func(context.Context, store.SkillHubPublicationDescriptionApplication, store.SkillHubPublicationDescriptionReleaseIntent, publicationDescriptionCloudReleasePermit) (*publicationDescriptionCloudReleaseAdmission, error) {
					admitCalls++
					if tc.nilResponse {
						return nil, nil
					}
					return admission, nil
				},
			}
			if got, err := admitConfirmedPublicationDescriptionReleaseFlow(context.Background(), flowDB, 1, db.app.Input.Receipt.Query.EntryID, ports); got != nil || err != store.ErrSkillHubPublicationConflict {
				t.Fatalf("invalid Cloud admission was accepted: got=%+v err=%v", got, err)
			}
			if admitCalls != 1 {
				t.Fatalf("Cloud admission called %d times, want exactly once", admitCalls)
			}
		})
	}
}

func TestAdmitConfirmedPublicationDescriptionReleaseFlowDoesNotAdmitChangedIntent(t *testing.T) {
	db, continuationPorts, _ := descriptionContinuationFixture(t)
	flowDB := &publicationDescriptionReleaseFlowStore{descriptionContinuationTestStore: db}
	checkpoint, err := continueConfirmedPublicationDescription(context.Background(), flowDB, 1, db.app.Input.Receipt.Query.EntryID, continuationPorts)
	if err != nil {
		t.Fatal(err)
	}
	plan := descriptionPlanFixture(checkpoint.checkpoint.Application, false)
	flowDB.intent = releaseFlowIntent(checkpoint.checkpoint, plan.Target)
	var permit publicationDescriptionCloudReleasePermit
	commitCalls := 0
	ports := publicationDescriptionReleasePorts{publicationDescriptionContinuationPorts: continuationPorts,
		PlanRetainedCopy: func(_ context.Context, app store.SkillHubPublicationDescriptionApplication, confirm bool) (*publicationDescriptionReleasePlan, error) {
			p := descriptionPlanFixture(app, confirm)
			return &p, nil
		},
		AuthorizeCloud: func(_ context.Context, _ store.SkillHubPublicationDescriptionApplication, intent store.SkillHubPublicationDescriptionReleaseIntent) (*publicationDescriptionCloudReleasePermit, error) {
			permit = publicationDescriptionCloudReleasePermit{SchemaVersion: 1, ApplicationID: intent.ApplicationID,
				EntryID: intent.EntryID, Target: intent.Target, IntentHash: intent.IdentityHash,
				PermitID: "pubdescpermit_" + strings.Repeat("c", 32), AuthorizedAt: intent.RequestedAt.Add(time.Second).Format(time.RFC3339Nano)}
			replayed := false
			permit.Replayed = &replayed
			flowDB.intent.Target.Version = "9.9.9"
			return &permit, nil
		},
		AdmitCloud: func(context.Context, store.SkillHubPublicationDescriptionApplication, store.SkillHubPublicationDescriptionReleaseIntent, publicationDescriptionCloudReleasePermit) (*publicationDescriptionCloudReleaseAdmission, error) {
			commitCalls++
			return nil, nil
		},
	}
	if got, err := admitConfirmedPublicationDescriptionReleaseFlow(context.Background(), flowDB, 1, db.app.Input.Receipt.Query.EntryID, ports); got != nil || err != store.ErrSkillHubPublicationConflict {
		t.Fatalf("changed intent reached Cloud admission: got=%+v err=%v", got, err)
	}
	if commitCalls != 0 {
		t.Fatalf("Cloud admission called %d times", commitCalls)
	}
}

func TestAuthorizeConfirmedPublicationDescriptionReleaseFlowReadsIntentBeforeCloud(t *testing.T) {
	db, continuationPorts, _ := descriptionContinuationFixture(t)
	flowDB := &publicationDescriptionReleaseFlowStore{descriptionContinuationTestStore: db}
	planCalls, cloudCalls := 0, 0
	ports := publicationDescriptionReleasePorts{publicationDescriptionContinuationPorts: continuationPorts,
		PlanRetainedCopy: func(context.Context, store.SkillHubPublicationDescriptionApplication, bool) (*publicationDescriptionReleasePlan, error) {
			planCalls++
			return nil, nil
		},
		AuthorizeCloud: func(context.Context, store.SkillHubPublicationDescriptionApplication, store.SkillHubPublicationDescriptionReleaseIntent) (*publicationDescriptionCloudReleasePermit, error) {
			cloudCalls++
			return nil, nil
		},
	}
	if got, err := authorizeConfirmedPublicationDescriptionReleaseFlow(context.Background(), flowDB, 1,
		db.app.Input.Receipt.Query.EntryID, ports); got != nil || err != store.ErrSkillHubPublicationConflict {
		t.Fatalf("missing durable intent was not rejected before Cloud: got=%v err=%v", got, err)
	}
	if planCalls != 0 || cloudCalls != 0 {
		t.Fatalf("Cloud was reached before durable intent validation: plan=%d authorize=%d", planCalls, cloudCalls)
	}
}

func releaseFlowIntent(checkpoint store.SkillHubPublicationDescriptionContinuation, target store.SkillHubPublicationCommitTarget) *store.SkillHubPublicationDescriptionReleaseIntent {
	intent := &store.SkillHubPublicationDescriptionReleaseIntent{EntryID: checkpoint.Draft.EntryID, ActorUID: 1,
		ApplicationID: checkpoint.Application.ID, AcceptanceIdentityHash: checkpoint.Acceptance.IdentityHash,
		Target: target, RequestedAt: checkpoint.Acceptance.AcceptedAt.Add(time.Second)}
	intent.IdentityHash, _ = intent.Digest()
	return intent
}

func TestAuthorizeConfirmedPublicationDescriptionReleaseFlowRequiresCloudAdapters(t *testing.T) {
	db, _, _ := descriptionContinuationFixture(t)
	flowDB := &publicationDescriptionReleaseFlowStore{descriptionContinuationTestStore: db}
	if got, err := authorizeConfirmedPublicationDescriptionReleaseFlow(context.Background(), flowDB, 1,
		db.app.Input.Receipt.Query.EntryID, publicationDescriptionReleasePorts{}); got != nil || err != store.ErrSkillHubPublicationInvalid {
		t.Fatalf("missing adapters were not rejected: got=%v err=%v", got, err)
	}
}

package server

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/openchat/openchat/server/store"
)

type repairMaterialReportReaderFunc func(context.Context, store.SkillHubPublicationCloudReceipt) (*PublicationCloudValidationReport, error)

func (f repairMaterialReportReaderFunc) Report(ctx context.Context, receipt store.SkillHubPublicationCloudReceipt) (*PublicationCloudValidationReport, error) {
	return f(ctx, receipt)
}

type repairOriginalMaterialReaderFunc func(context.Context, store.SkillHubPublicationCloudReceipt) ([]byte, error)

func (f repairOriginalMaterialReaderFunc) readOriginal(ctx context.Context, receipt store.SkillHubPublicationCloudReceipt) ([]byte, error) {
	return f(ctx, receipt)
}

func TestPreparePublicationRepairModelMaterialUsesExactTrustedEvidence(t *testing.T) {
	raw, task, report := projectionTestInput(t, []projectionTestFile{{"notes.txt", "untrusted source", false}, {"SKILL.md", "---\nname: reader\n---\n", true}})
	wantTask := snapshotPublicationRepairTask(task)
	var reports, reads int
	prepared, err := preparePublicationRepairModelMaterial(context.Background(), task,
		repairMaterialReportReaderFunc(func(_ context.Context, got store.SkillHubPublicationCloudReceipt) (*PublicationCloudValidationReport, error) {
			reports++
			if got != task.Input.Receipt {
				t.Fatal("report read did not use stored receipt")
			}
			return &PublicationCloudValidationReport{SchemaVersion: 1, Receipt: publicationControlReceipt(got), Validation: report}, nil
		}),
		repairOriginalMaterialReaderFunc(func(_ context.Context, got store.SkillHubPublicationCloudReceipt) ([]byte, error) {
			reads++
			if got != task.Input.Receipt {
				t.Fatal("material read did not use stored receipt")
			}
			return append([]byte(nil), raw...), nil
		}))
	if err != nil || reports != 1 || reads != 1 || !reflect.DeepEqual(task, wantTask) || prepared.Report.CandidateHash != report.CandidateHash || prepared.SourceText == "" {
		t.Fatal("trusted material preparation failed", err)
	}
	*prepared.Report.Issues[0].Location.EntryIndex = 90
	if *report.Issues[0].Location.EntryIndex == 90 {
		t.Fatal("prepared report aliases Cloud response")
	}
}

func TestPreparePublicationRepairModelMaterialRejectsMismatchedEvidenceBeforeRead(t *testing.T) {
	raw, task, report := projectionTestInput(t, []projectionTestFile{{"SKILL.md", "---\nname: reader\n---\n", false}})
	tests := []struct {
		name   string
		change func(*PublicationCloudValidationReport)
	}{
		{"receipt", func(r *PublicationCloudValidationReport) {
			r.Receipt.RequestHash = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
		}},
		{"candidate", func(r *PublicationCloudValidationReport) {
			r.Validation.CandidateHash = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
		}},
		{"passed", func(r *PublicationCloudValidationReport) {
			r.Validation.Outcome = "passed"
			r.Validation.NextAction = "none"
			r.Validation.Issues = nil
		}},
		{"truncated", func(r *PublicationCloudValidationReport) { r.Validation.Truncated = true }},
		{"issue-set", func(r *PublicationCloudValidationReport) { r.Validation.Issues[0].Code = "format.other" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reads := 0
			_, err := preparePublicationRepairModelMaterial(context.Background(), task,
				repairMaterialReportReaderFunc(func(_ context.Context, receipt store.SkillHubPublicationCloudReceipt) (*PublicationCloudValidationReport, error) {
					out := &PublicationCloudValidationReport{SchemaVersion: 1, Receipt: publicationControlReceipt(receipt), Validation: snapshotPublicationRepairModelReport(report)}
					tt.change(out)
					return out, nil
				}),
				repairOriginalMaterialReaderFunc(func(context.Context, store.SkillHubPublicationCloudReceipt) ([]byte, error) {
					reads++
					return raw, nil
				}))
			if err == nil || reads != 0 {
				t.Fatal("mismatched report reached candidate reader", err, reads)
			}
		})
	}
}

func TestPreparePublicationRepairModelMaterialRejectsUntrustedTaskAndPeerErrors(t *testing.T) {
	_, task, _ := projectionTestInput(t, []projectionTestFile{{"SKILL.md", "---\nname: reader\n---\n", false}})
	reads := 0
	reports := repairMaterialReportReaderFunc(func(context.Context, store.SkillHubPublicationCloudReceipt) (*PublicationCloudValidationReport, error) {
		reads++
		return nil, errors.New("private upstream detail")
	})
	materials := repairOriginalMaterialReaderFunc(func(context.Context, store.SkillHubPublicationCloudReceipt) ([]byte, error) {
		t.Fatal("candidate read after failed report")
		return nil, nil
	})
	bad := snapshotPublicationRepairTask(task)
	bad.Input.TopicID = "grp_456"
	if _, err := preparePublicationRepairModelMaterial(context.Background(), bad, reports, materials); err == nil || reads != 0 {
		t.Fatal("untrusted task was read", err)
	}
	if _, err := preparePublicationRepairModelMaterial(context.Background(), task, reports, materials); !errors.Is(err, errPublicationRepairMaterialPreparation) || reads != 1 {
		t.Fatal("private peer error was not mapped to a static error", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := preparePublicationRepairModelMaterial(ctx, task, reports, materials); !errors.Is(err, context.Canceled) || reads != 1 {
		t.Fatal("cancelled preparation performed peer work", err)
	}
}

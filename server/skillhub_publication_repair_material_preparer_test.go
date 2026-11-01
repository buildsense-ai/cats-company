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
		{"schema-version", func(r *PublicationCloudValidationReport) { r.SchemaVersion = 0 }},
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
	raw, task, report := projectionTestInput(t, []projectionTestFile{{"SKILL.md", "---\nname: reader\n---\n", false}})
	reportReads, materialReads := 0, 0
	validReports := repairMaterialReportReaderFunc(func(_ context.Context, receipt store.SkillHubPublicationCloudReceipt) (*PublicationCloudValidationReport, error) {
		reportReads++
		return &PublicationCloudValidationReport{SchemaVersion: 1, Receipt: publicationControlReceipt(receipt), Validation: snapshotPublicationRepairModelReport(report)}, nil
	})
	validMaterials := repairOriginalMaterialReaderFunc(func(context.Context, store.SkillHubPublicationCloudReceipt) ([]byte, error) {
		materialReads++
		return append([]byte(nil), raw...), nil
	})
	bad := snapshotPublicationRepairTask(task)
	bad.Rounds[0].Lease.Token = "invalid"
	if _, err := preparePublicationRepairModelMaterial(context.Background(), bad, validReports, validMaterials); err == nil || reportReads != 0 || materialReads != 0 {
		t.Fatal("invalid lease reached peer readers", err, reportReads, materialReads)
	}
	var nilReportReader *PublicationCommitCloudClient
	var nilMaterialReader *publicationRepairMaterialClient
	if _, err := preparePublicationRepairModelMaterial(context.Background(), task, nilReportReader, validMaterials); err == nil || reportReads != 0 || materialReads != 0 {
		t.Fatal("typed-nil report reader was not rejected", err)
	}
	if _, err := preparePublicationRepairModelMaterial(context.Background(), task, validReports, nilMaterialReader); err == nil || reportReads != 0 || materialReads != 0 {
		t.Fatal("typed-nil material reader was not rejected", err)
	}
	if _, err := preparePublicationRepairModelMaterial(context.Background(), task,
		repairMaterialReportReaderFunc(func(context.Context, store.SkillHubPublicationCloudReceipt) (*PublicationCloudValidationReport, error) {
			return nil, errors.New("private report upstream detail")
		}), validMaterials); !errors.Is(err, errPublicationRepairMaterialPeer) || err.Error() == "private report upstream detail" || materialReads != 0 {
		t.Fatal("private report error was not mapped to a static error", err)
	}
	if _, err := preparePublicationRepairModelMaterial(context.Background(), task, validReports,
		repairOriginalMaterialReaderFunc(func(context.Context, store.SkillHubPublicationCloudReceipt) ([]byte, error) {
			return nil, errors.New("private material upstream detail")
		})); !errors.Is(err, errPublicationRepairMaterialPeer) || err.Error() == "private material upstream detail" {
		t.Fatal("private material error was not mapped to a static error", err)
	}
	corrupt := append([]byte(nil), raw...)
	corrupt[len(corrupt)-1] ^= 1
	if _, err := preparePublicationRepairModelMaterial(context.Background(), task, validReports,
		repairOriginalMaterialReaderFunc(func(context.Context, store.SkillHubPublicationCloudReceipt) ([]byte, error) { return corrupt, nil })); !errors.Is(err, errPublicationRepairMaterialPeer) {
		t.Fatal("projector error was not mapped to a static error", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	beforeReports, beforeMaterials := reportReads, materialReads
	if _, err := preparePublicationRepairModelMaterial(ctx, task, validReports, validMaterials); !errors.Is(err, context.Canceled) || reportReads != beforeReports || materialReads != beforeMaterials {
		t.Fatal("cancelled preparation performed peer work", err)
	}
}

func TestPreparePublicationRepairModelMaterialHonorsCancellationAfterPeerReads(t *testing.T) {
	raw, task, report := projectionTestInput(t, []projectionTestFile{{"SKILL.md", "---\nname: reader\n---\n", false}})
	t.Run("report", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		reads := 0
		_, err := preparePublicationRepairModelMaterial(ctx, task,
			repairMaterialReportReaderFunc(func(_ context.Context, receipt store.SkillHubPublicationCloudReceipt) (*PublicationCloudValidationReport, error) {
				cancel()
				return &PublicationCloudValidationReport{SchemaVersion: 1, Receipt: publicationControlReceipt(receipt), Validation: report}, nil
			}),
			repairOriginalMaterialReaderFunc(func(context.Context, store.SkillHubPublicationCloudReceipt) ([]byte, error) { reads++; return raw, nil }))
		if !errors.Is(err, context.Canceled) || reads != 0 {
			t.Fatal("report cancellation did not stop before candidate read", err, reads)
		}
	})
	t.Run("material", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		_, err := preparePublicationRepairModelMaterial(ctx, task,
			repairMaterialReportReaderFunc(func(_ context.Context, receipt store.SkillHubPublicationCloudReceipt) (*PublicationCloudValidationReport, error) {
				return &PublicationCloudValidationReport{SchemaVersion: 1, Receipt: publicationControlReceipt(receipt), Validation: report}, nil
			}),
			repairOriginalMaterialReaderFunc(func(context.Context, store.SkillHubPublicationCloudReceipt) ([]byte, error) {
				cancel()
				return raw, nil
			}))
		if !errors.Is(err, context.Canceled) {
			t.Fatal("material cancellation was not returned", err)
		}
	})
}

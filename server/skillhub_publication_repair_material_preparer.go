package server

import (
	"context"
	"reflect"

	"github.com/openchat/openchat/server/store"
)

type publicationRepairValidationReportReader interface {
	Report(context.Context, store.SkillHubPublicationCloudReceipt) (*PublicationCloudValidationReport, error)
}

type publicationRepairOriginalMaterialReader interface {
	readOriginal(context.Context, store.SkillHubPublicationCloudReceipt) ([]byte, error)
}

// preparePublicationRepairModelMaterial combines only trusted service reads.
// The caller must bracket it with current task/lease and Runtime source checks;
// this function is not itself authority to read or execute a Skill.
func preparePublicationRepairModelMaterial(ctx context.Context, task store.SkillHubPublicationRepairTask, reports publicationRepairValidationReportReader, materials publicationRepairOriginalMaterialReader) (publicationRepairModelMaterial, error) {
	if nilPublicationRepairReader(reports) || nilPublicationRepairReader(materials) || len(task.Rounds) != 1 ||
		task.Rounds[0].Lease.Validate() != nil ||
		!validPublicationRepairModelTask(task, task.Rounds[0].Lease) {
		return publicationRepairModelMaterial{}, store.ErrSkillHubPublicationInvalid
	}
	if err := ctx.Err(); err != nil {
		return publicationRepairModelMaterial{}, err
	}
	receipt := task.Input.Receipt
	cloudReport, err := reports.Report(ctx, receipt)
	if err != nil {
		if ctx.Err() != nil {
			return publicationRepairModelMaterial{}, ctx.Err()
		}
		return publicationRepairModelMaterial{}, errPublicationRepairMaterialPeer
	}
	if cloudReport == nil || cloudReport.SchemaVersion != 1 || cloudReport.Receipt != publicationControlReceipt(receipt) ||
		!validPublicationValidationReport(cloudReport.Validation, receipt.Query.SourceSnapshotHash) ||
		cloudReport.Validation.Outcome != "rejected" || cloudReport.Validation.Truncated {
		return publicationRepairModelMaterial{}, errPublicationRepairMaterialPeer
	}
	evidence, err := publicationRepairEvidence(cloudReport.Validation, receipt.Query.SourceSnapshotHash)
	if err != nil || evidence != task.Input.Evidence {
		return publicationRepairModelMaterial{}, errPublicationRepairMaterialPeer
	}
	if err := ctx.Err(); err != nil {
		return publicationRepairModelMaterial{}, err
	}
	raw, err := materials.readOriginal(ctx, receipt)
	if err != nil {
		if ctx.Err() != nil {
			return publicationRepairModelMaterial{}, ctx.Err()
		}
		return publicationRepairModelMaterial{}, errPublicationRepairMaterialPeer
	}
	material, err := projectPublicationRepairMaterial(ctx, snapshotPublicationRepairTask(task), raw, cloudReport.Validation)
	if err != nil {
		if ctx.Err() != nil {
			return publicationRepairModelMaterial{}, ctx.Err()
		}
		return publicationRepairModelMaterial{}, errPublicationRepairMaterialPeer
	}
	return material, nil
}

func nilPublicationRepairReader(reader any) bool {
	if reader == nil {
		return true
	}
	v := reflect.ValueOf(reader)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

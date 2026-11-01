package server

import (
	"context"
	"errors"

	"github.com/openchat/openchat/server/store"
)

var errPublicationRepairMaterialPreparation = errors.New("publication.repair_material_unavailable")

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
	if reports == nil || materials == nil || len(task.Rounds) != 1 ||
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
		return publicationRepairModelMaterial{}, errPublicationRepairMaterialPreparation
	}
	if cloudReport == nil || cloudReport.Receipt != publicationControlReceipt(receipt) ||
		!validPublicationValidationReport(cloudReport.Validation, receipt.Query.SourceSnapshotHash) ||
		cloudReport.Validation.Outcome != "rejected" || cloudReport.Validation.Truncated {
		return publicationRepairModelMaterial{}, errPublicationRepairMaterialPreparation
	}
	evidence, err := publicationRepairEvidence(cloudReport.Validation, receipt.Query.SourceSnapshotHash)
	if err != nil || evidence != task.Input.Evidence {
		return publicationRepairModelMaterial{}, errPublicationRepairMaterialPreparation
	}
	if err := ctx.Err(); err != nil {
		return publicationRepairModelMaterial{}, err
	}
	raw, err := materials.readOriginal(ctx, receipt)
	if err != nil {
		if ctx.Err() != nil {
			return publicationRepairModelMaterial{}, ctx.Err()
		}
		return publicationRepairModelMaterial{}, errPublicationRepairMaterialPreparation
	}
	material, err := projectPublicationRepairMaterial(ctx, snapshotPublicationRepairTask(task), raw, cloudReport.Validation)
	if err != nil {
		if ctx.Err() != nil {
			return publicationRepairModelMaterial{}, ctx.Err()
		}
		return publicationRepairModelMaterial{}, errPublicationRepairMaterialPreparation
	}
	return material, nil
}

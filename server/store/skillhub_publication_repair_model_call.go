package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
	"unicode/utf8"
)

const SkillHubPublicationRepairModelOutputMaxBytes = 64 * 1024

// RequestHash is computed by the trusted executor over the COMPLETE immutable
// model request, including payer, provider/model/configuration, policy version,
// prompt and bounded source/report inputs. It is not supplied by a model or UI.
// Do not include credentials in the retained record or log the request/output.
type SkillHubPublicationRepairModelCallInput struct {
	RequestHash string
}

func (in SkillHubPublicationRepairModelCallInput) Validate() error {
	if !publicationSHA256.MatchString(in.RequestHash) {
		return ErrSkillHubPublicationInvalid
	}
	return nil
}

// This is untrusted model TEXT, not an approved description, patch, tool call,
// validation result or permission to write files. Consumers must parse and
// independently constrain it. Only private internal stores may return it.
type SkillHubPublicationRepairModelOutput struct {
	Text string `json:"-"`
}

func (o SkillHubPublicationRepairModelOutput) Validate() error {
	if len(o.Text) == 0 || len(o.Text) > SkillHubPublicationRepairModelOutputMaxBytes ||
		!utf8.ValidString(o.Text) || strings.ContainsRune(o.Text, 0) {
		return ErrSkillHubPublicationInvalid
	}
	return nil
}

func (o SkillHubPublicationRepairModelOutput) Hash() string {
	h := sha256.Sum256([]byte(o.Text))
	return hex.EncodeToString(h[:])
}

// One immutable dispatch fence per retained repair round. DispatchLease is the
// original lease, never rewritten during round takeover. A missing output is
// UNKNOWN, not proof that no model request/charge occurred.
type SkillHubPublicationRepairModelCall struct {
	CallID        string
	DispatchLease SkillHubPublicationRepairLease
	Input         SkillHubPublicationRepairModelCallInput
	CreatedAt     time.Time
	Output        *SkillHubPublicationRepairModelOutput `json:"-"`
	OutputHash    string
	CompletedAt   *time.Time
}

func (c SkillHubPublicationRepairModelCall) Validate() error {
	if !strings.HasPrefix(c.CallID, "pubmodel_") || len(c.CallID) != 41 ||
		!publicationRepairToken.MatchString(strings.TrimPrefix(c.CallID, "pubmodel_")) ||
		c.DispatchLease.Validate() != nil || c.Input.Validate() != nil || c.CreatedAt.IsZero() ||
		!c.DispatchLease.Until.After(c.CreatedAt) {
		return ErrSkillHubPublicationConflict
	}
	if c.Output == nil {
		if c.OutputHash != "" || c.CompletedAt != nil {
			return ErrSkillHubPublicationConflict
		}
	} else if c.Output.Validate() != nil || c.OutputHash != c.Output.Hash() || c.CompletedAt == nil ||
		c.CompletedAt.Before(c.CreatedAt) || !c.DispatchLease.Until.After(*c.CompletedAt) {
		return ErrSkillHubPublicationConflict
	}
	return nil
}

// Internal journal only; no registered route, RPC, worker, model invocation,
// provider retry, file writer or release authority. PostgreSQL only.
//
// Start's bool is true ONLY for the transaction that first commits the fence.
// Only that caller may attempt ONE provider request, after current Runtime/
// authority checks. A nil/failed/lost reply gives no dispatch permission. Replay,
// restart and lease takeover always return false, including with missing output;
// callers MUST recover output or stop model_result_uncertain, never retry a paid
// request. This is at-most-one dispatch authorization, not exactly-once billing.
//
// Completion requires the original still-current live lease; uncertain or late
// results cannot overwrite a reclaimed round. Get is private original-active-
// publisher history, NOT an execution grant. A new executor must recheck current
// authority/source and its round before consuming a recovered output.
type SkillHubPublicationRepairModelCallStore interface {
	StartSkillHubPublicationRepairModelCall(context.Context, int64, SkillHubPublicationRepairLease, SkillHubPublicationRepairModelCallInput) (*SkillHubPublicationRepairModelCall, bool, error)
	CompleteSkillHubPublicationRepairModelCall(context.Context, int64, SkillHubPublicationRepairLease, string, SkillHubPublicationRepairModelOutput) (*SkillHubPublicationRepairModelCall, error)
	GetSkillHubPublicationRepairModelCall(context.Context, int64, string, int) (*SkillHubPublicationRepairModelCall, error)
}

// Current execution authority, unlike private ledger/model-call history. The
// original active human, Bot/body, task/topic/membership and exact unfinished
// live lease are checked transactionally. It grants no model dispatch permission.
type SkillHubPublicationRepairExecutionReader interface {
	GetSkillHubPublicationRepairExecutionTask(context.Context, int64, SkillHubPublicationRepairLease) (*SkillHubPublicationRepairTask, error)
}

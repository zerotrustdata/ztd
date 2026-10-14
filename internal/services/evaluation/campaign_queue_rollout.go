// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// Rollout queue entry statuses.
const (
	QueueStatusPending  = "pending"
	QueueStatusVerified = "verified"
	QueueStatusFailed   = "failed"
	QueueStatusSkipped  = "skipped"
)

const rolloutQueueGenerateCommand = "./ztd eval rollout add <selector>"

// LoadRolloutQueue reads the rollout queue. A queue that has not been written
// yet is empty rather than an error.
func LoadRolloutQueue(ctx context.Context, fileSvc fs.RuntimeFileService) (*CampaignQueue, error) {
	if fileSvc == nil {
		return nil, fmt.Errorf("evaluation: load rollout queue: %w", constants.ErrMissingRequiredField)
	}
	exists, err := fileSvc.FileExists(ctx, DefaultInitCampaignQueueRelPath)
	if err != nil {
		return nil, fmt.Errorf("evaluation: load rollout queue: %w", err)
	}
	if !exists {
		return BuildInitCampaignQueue("", nil), nil
	}
	return LoadInitCampaignQueueFromRuntime(ctx, fileSvc, DefaultInitCampaignQueueRelPath)
}

// LockRolloutQueue takes the rollout queue's writer lock. Every queue edit
// (add and the selector verbs) holds it across its load/modify/save, and
// rollout run holds it from planning to the end of the batch, so no edit can
// overwrite an outcome the run records or be overwritten by one. A held lock
// fails with ErrRuntimeFileLocked and changes nothing. The run's own entry
// updates (MarkCampaignQueueEntry) happen under its lock and do not retake it.
func LockRolloutQueue(ctx context.Context, fileSvc fs.RuntimeFileService) (io.Closer, error) {
	if fileSvc == nil {
		return nil, fmt.Errorf("evaluation: lock rollout queue: %w", constants.ErrMissingRequiredField)
	}
	held, err := fileSvc.TryLock(ctx, constants.EvaluationRolloutQueueLockRelPath, constants.PermFilePrivate)
	if errors.Is(err, constants.ErrRuntimeFileLocked) {
		return nil, fmt.Errorf("evaluation: the rollout queue is in use by a rollout run or another queue edit; retry when it finishes: %w", err)
	}
	if err != nil {
		return nil, fmt.Errorf("evaluation: lock rollout queue: %w", err)
	}
	return held, nil
}

// SaveRolloutQueue writes the rollout queue. An empty queue removes the file.
func SaveRolloutQueue(ctx context.Context, fileSvc fs.RuntimeFileService, queue *CampaignQueue) error {
	if fileSvc == nil || queue == nil {
		return fmt.Errorf("evaluation: save rollout queue: %w", constants.ErrMissingRequiredField)
	}
	if len(queue.Models) == 0 {
		if err := fileSvc.Remove(ctx, DefaultInitCampaignQueueRelPath); err != nil && !errors.Is(err, constants.ErrNotFound) {
			return fmt.Errorf("evaluation: save rollout queue: %w", err)
		}
		return nil
	}
	return SaveInitCampaignQueueToRuntime(ctx, fileSvc, DefaultInitCampaignQueueRelPath, queue)
}

// LoadRolloutIntakePriorityIDs reads the rollout ordering from the project
// root. A missing file means no priority. A malformed file returns an error.
func LoadRolloutIntakePriorityIDs(projectRoot string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(projectRoot, DefaultRolloutIntakePriorityRelPath))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("evaluation: load rollout intake priority: %w", err)
	}
	var priority []string
	if err := json.Unmarshal(data, &priority); err != nil {
		return nil, fmt.Errorf("evaluation: load rollout intake priority: %w", err)
	}
	return priority, nil
}

// NewCampaignQueueModel builds the pending queue entry for one registry model.
func NewCampaignQueueModel(variant *evalv1.ModelVariant) (CampaignQueueModel, error) {
	if variant == nil {
		return CampaignQueueModel{}, fmt.Errorf("evaluation: new campaign queue model: %w", constants.ErrMissingRequiredField)
	}
	campaignID := CampaignIDForVariant(variant)
	freeze, err := MaterializeModelRegistry(campaignID, []*evalv1.ModelVariant{variant})
	if err != nil {
		return CampaignQueueModel{}, err
	}
	return CampaignQueueModel{
		VariantID:            variant.GetVariantId(),
		ServedModelTag:       variant.GetServedModelTag(),
		CampaignID:           campaignID,
		InventoryFile:        campaignInventoryPath(campaignID),
		ModelRegistryDigest:  freeze.RegistryDigest,
		HomogeneousCellCount: freeze.HomogeneousCellCount,
		Status:               QueueStatusPending,
	}, nil
}

// Reasons rollout add records for a selected variant it does not queue.
const (
	RolloutSkipToolCallingUnsupported = "tool_calling_unsupported"
	RolloutSkipAlias                  = "alias"
)

// RolloutIntakeSkip is a selected variant that rollout add did not queue.
// AliasOf names the served tag already queued for the same model digest.
type RolloutIntakeSkip struct {
	VariantID      string `json:"variant_id"`
	ServedModelTag string `json:"served_model_tag"`
	Reason         string `json:"reason"`
	AliasOf        string `json:"alias_of,omitempty"`
}

// RolloutIntake is the outcome of adding variants to the rollout queue.
type RolloutIntake struct {
	Added         []CampaignQueueModel
	Skipped       []RolloutIntakeSkip
	AlreadyQueued int
}

// AddVariants appends the variants that are not queued yet, ordered by the
// rollout ordering (priority intake first, then smallest models first), and
// returns the entries it added. Queued entries keep their place and status.
//
// A variant whose recorded tool-calling probe the provider rejected is skipped:
// every scenario declares tools, so its run could only fail at the provider.
// A variant that shares a model digest with a queued entry, or with a variant
// added earlier in rollout order, is an alias of that model and is skipped.
// registry resolves the model digests of queued entries.
func (queue *CampaignQueue) AddVariants(variants, registry []*evalv1.ModelVariant, priorityIDs []string) (RolloutIntake, error) {
	if queue == nil {
		return RolloutIntake{}, fmt.Errorf("evaluation: rollout queue add: %w", constants.ErrMissingRequiredField)
	}
	queued := make(map[string]struct{}, len(queue.Models))
	for _, entry := range queue.Models {
		queued[entry.VariantID] = struct{}{}
	}
	digestOwners := make(map[string]string, len(queue.Models))
	for _, variant := range registry {
		if _, ok := queued[variant.GetVariantId()]; ok && variant.GetModelDigest() != "" {
			digestOwners[variant.GetModelDigest()] = variant.GetServedModelTag()
		}
	}
	var intake RolloutIntake
	fresh := make([]*evalv1.ModelVariant, 0, len(variants))
	for _, variant := range variants {
		if variant == nil {
			continue
		}
		if _, ok := queued[variant.GetVariantId()]; ok {
			intake.AlreadyQueued++
			continue
		}
		queued[variant.GetVariantId()] = struct{}{}
		fresh = append(fresh, variant)
	}
	SortModelVariantsForRollout(fresh)
	fresh = PrioritizeRolloutIntake(fresh, priorityIDs)
	intake.Added = make([]CampaignQueueModel, 0, len(fresh))
	for _, variant := range fresh {
		skip := RolloutIntakeSkip{VariantID: variant.GetVariantId(), ServedModelTag: variant.GetServedModelTag()}
		if ToolCallingUnsupported(variant) {
			skip.Reason = RolloutSkipToolCallingUnsupported
			intake.Skipped = append(intake.Skipped, skip)
			continue
		}
		if owner, ok := digestOwners[variant.GetModelDigest()]; ok {
			skip.Reason = RolloutSkipAlias
			skip.AliasOf = owner
			intake.Skipped = append(intake.Skipped, skip)
			continue
		}
		entry, err := NewCampaignQueueModel(variant)
		if err != nil {
			return RolloutIntake{}, err
		}
		digestOwners[variant.GetModelDigest()] = variant.GetServedModelTag()
		intake.Added = append(intake.Added, entry)
	}
	queue.Models = append(queue.Models, intake.Added...)
	return intake, nil
}

// ToolCallingUnsupported reports whether the variant's recorded tool-calling
// capability probe was rejected by the provider. A variant frozen without
// probes has no observation and is not excluded.
func ToolCallingUnsupported(variant *evalv1.ModelVariant) bool {
	for _, observation := range variant.GetCapabilityObservations() {
		if observation.GetCapability() == evalv1.ModelCapabilityKind_MODEL_CAPABILITY_KIND_TOOL_CALLING {
			return observation.GetOutcome() == evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_UNSUPPORTED
		}
	}
	return false
}

// SelectEntries returns the queue entries whose served tag or variant ID is
// named in ids. An unknown ID is an error.
func (queue *CampaignQueue) SelectEntries(ids []string) ([]CampaignQueueModel, error) {
	selected := make([]CampaignQueueModel, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		entry, err := queue.FindByTagOrVariantID(id)
		if err != nil {
			return nil, fmt.Errorf("evaluation: rollout queue select: %w: %s", constants.ErrInferenceModelNotFound, id)
		}
		if _, dup := seen[entry.VariantID]; dup {
			continue
		}
		seen[entry.VariantID] = struct{}{}
		selected = append(selected, *entry)
	}
	return selected, nil
}

// EntriesForVariants returns the queue entries of the given variants, in queue
// order. Variants that are not queued are ignored.
func (queue *CampaignQueue) EntriesForVariants(variants []*evalv1.ModelVariant) []CampaignQueueModel {
	want := make(map[string]struct{}, len(variants))
	for _, variant := range variants {
		if variant != nil {
			want[variant.GetVariantId()] = struct{}{}
		}
	}
	entries := make([]CampaignQueueModel, 0, len(want))
	for _, entry := range queue.Models {
		if _, ok := want[entry.VariantID]; ok {
			entries = append(entries, entry)
		}
	}
	return entries
}

// RemoveEntries drops the entries with the given variant IDs.
func (queue *CampaignQueue) RemoveEntries(variantIDs []string) {
	drop := make(map[string]struct{}, len(variantIDs))
	for _, id := range variantIDs {
		drop[id] = struct{}{}
	}
	kept := queue.Models[:0:0]
	for _, entry := range queue.Models {
		if _, gone := drop[entry.VariantID]; !gone {
			kept = append(kept, entry)
		}
	}
	queue.Models = kept
}

// SetEntryStatus sets the status and note of the entries with the given variant
// IDs. A note replaces the previous note; an empty note clears it.
func (queue *CampaignQueue) SetEntryStatus(variantIDs []string, status, notes string) {
	want := make(map[string]struct{}, len(variantIDs))
	for _, id := range variantIDs {
		want[id] = struct{}{}
	}
	for i, entry := range queue.Models {
		if _, ok := want[entry.VariantID]; !ok {
			continue
		}
		queue.Models[i].Status = status
		queue.Models[i].Notes = strings.TrimSpace(notes)
		if status != QueueStatusVerified {
			queue.Models[i].VerifiedRunID = ""
		}
	}
}

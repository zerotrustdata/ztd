// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// MaterializeCampaignInventoryRequest writes a multi-model inventory freeze file.
type MaterializeCampaignInventoryRequest struct {
	Context     context.Context
	FileService fs.RuntimeFileService
	CampaignID  string
	OutputPath  string
	Variants    []*evalv1.ModelVariant
}

// MaterializeCampaignInventory freezes one or more variants into a single inventory file.
func MaterializeCampaignInventory(req MaterializeCampaignInventoryRequest) (*ModelInventoryFreeze, string, error) {
	if req.FileService == nil || req.CampaignID == "" || len(req.Variants) == 0 {
		return nil, "", fmt.Errorf("evaluation: materialize campaign inventory: %w", constants.ErrMissingRequiredField)
	}
	freeze, err := MaterializeModelRegistry(req.CampaignID, req.Variants)
	if err != nil {
		return nil, "", err
	}
	if err := ValidateModelRegistry(freeze); err != nil {
		return nil, "", err
	}

	outputPath := req.OutputPath
	if outputPath == "" {
		outputPath = campaignInventoryPath(req.CampaignID)
	}
	outputPath = filepath.ToSlash(outputPath)
	if req.Context == nil {
		req.Context = context.Background()
	}
	payload, err := marshalModelInventoryFreeze(freeze)
	if err != nil {
		return nil, "", err
	}
	if err := req.FileService.WriteFile(req.Context, outputPath, payload, constants.PermFileReadOnly); err != nil {
		return nil, "", fmt.Errorf("evaluation: materialize campaign inventory: write: %w", err)
	}
	return freeze, outputPath, nil
}

// BuildInitCampaignQueue constructs a rollout queue manifest from entries.
func BuildInitCampaignQueue(inventoryRelDir string, entries []CampaignQueueModel) *CampaignQueue {
	if inventoryRelDir == "" {
		inventoryRelDir = DefaultCampaignInventoryRelDirname
	}
	return &CampaignQueue{
		Pattern:         InitCampaignIDPrefix + "<variant_id> (legacy " + LegacyInitCampaignID + " for " + LegacyInitCampaignServedModelTag + ")",
		CellsPerRun:     ComputeHomogeneousMatrixSize(1),
		CellsPerRunL2:   ComputeHomogeneousMatrixSizeForPosture(1, evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_CONSENSUS),
		InventoryDir:    filepath.ToSlash(inventoryRelDir),
		GenerateCommand: rolloutQueueGenerateCommand,
		Models:          append([]CampaignQueueModel(nil), entries...),
	}
}

// MarkCampaignQueueEntryRequest updates one queue entry after verification.
type MarkCampaignQueueEntryRequest struct {
	Context        context.Context
	FileService    fs.RuntimeFileService
	QueuePath      string
	VariantID      string
	ServedModelTag string
	Status         string
	VerifiedRunID  string
	Notes          string
}

// MarkCampaignQueueEntry updates and persists one queue entry.
func MarkCampaignQueueEntry(req MarkCampaignQueueEntryRequest) (*CampaignQueueModel, error) {
	if req.FileService == nil || req.Status == "" {
		return nil, fmt.Errorf("evaluation: mark campaign queue entry: %w", constants.ErrMissingRequiredField)
	}
	if req.Context == nil {
		req.Context = context.Background()
	}
	queuePath := req.QueuePath
	if queuePath == "" {
		queuePath = DefaultInitCampaignQueueRelPath
	}
	queue, err := LoadInitCampaignQueueFromRuntime(req.Context, req.FileService, queuePath)
	if err != nil {
		return nil, err
	}
	entry, err := queue.markEntry(req)
	if err != nil {
		return nil, err
	}
	err = SaveInitCampaignQueueToRuntime(req.Context, req.FileService, queuePath, queue)
	if err != nil {
		return nil, err
	}
	return entry, nil
}

func (queue *CampaignQueue) markEntry(req MarkCampaignQueueEntryRequest) (*CampaignQueueModel, error) {
	queryVariant := strings.TrimSpace(req.VariantID)
	queryTag := strings.TrimSpace(req.ServedModelTag)
	if queryVariant == "" && queryTag == "" {
		return nil, fmt.Errorf("evaluation: mark campaign queue entry: variant_id or served model tag: %w", constants.ErrMissingRequiredField)
	}
	for i, entry := range queue.Models {
		if queryVariant != "" && entry.VariantID != queryVariant {
			continue
		}
		if queryTag != "" && entry.ServedModelTag != queryTag {
			continue
		}
		queue.Models[i].Status = req.Status
		if req.VerifiedRunID != "" {
			queue.Models[i].VerifiedRunID = req.VerifiedRunID
		}
		if req.Notes != "" {
			queue.Models[i].Notes = req.Notes
		}
		selected := queue.Models[i]
		return &selected, nil
	}
	return nil, fmt.Errorf("evaluation: mark campaign queue entry: model: %w", constants.ErrNotFound)
}

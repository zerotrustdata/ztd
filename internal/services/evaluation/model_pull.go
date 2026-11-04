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
	"strings"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// ModelPullRequest stages models on the provider through the governed Inference
// Operator session.
type ModelPullRequest struct {
	CatalogPath        string
	Timeout            time.Duration
	VariantIDs         []string
	DryRun             bool
	Progress           func(ModelPullEvent)
	Dispatcher         OllamaModelCommandDispatcher
	InferenceSessionID string
	NewID              func(string) string
	CaseID             string
}

// ModelPull* event actions and statuses.
const (
	ModelPullActionPull    = "pull"
	ModelPullActionAlias   = "alias"
	ModelPullActionSkip    = "skip"
	ModelPullStatusStart   = "starting"
	ModelPullStatusSuccess = "success"
	ModelPullStatusFailed  = "failed"
)

// ModelPullEvent reports one pull, alias, or skip step for one catalog entry as
// it happens.
type ModelPullEvent struct {
	VariantID      string
	ServedModelTag string
	Action         string
	Status         string
	Detail         string
}

// ModelPullResult summarizes a model pull run.
type ModelPullResult struct {
	Pulled  []string
	Aliased []string
	Skipped []ModelPullSkip
	Failed  []ModelPullFailure
}

// ModelPullSkip records a deliberately skipped catalog entry.
type ModelPullSkip struct {
	VariantID      string
	ServedModelTag string
	Reason         string
}

// ModelPullFailure records a failed pull or alias. Err is the rendered failure
// for JSON output; Cause keeps its typed identity for errors.Is.
type ModelPullFailure struct {
	VariantID      string
	ServedModelTag string
	Err            string
	Cause          error `json:"-"`
}

func validateModelPullRequest(req ModelPullRequest) error {
	if req.Dispatcher == nil || req.InferenceSessionID == "" || req.NewID == nil {
		return fmt.Errorf("evaluation: pull models: governed inference session and dispatcher: %w", constants.ErrMissingRequiredField)
	}
	if _, err := pullTimeoutSeconds(req.Timeout); err != nil {
		return fmt.Errorf("evaluation: pull models: %w", err)
	}
	return nil
}

// PullCatalogModels pulls the selected pull catalog models through the governed
// Inference Operator and applies their canonical served-model aliases.
func PullCatalogModels(ctx context.Context, req ModelPullRequest) (*ModelPullResult, error) {
	if err := validateModelPullRequest(req); err != nil {
		return nil, err
	}
	catalog, err := LoadModelPullCatalog(req.CatalogPath)
	if err != nil {
		return nil, err
	}
	return pullModels(ctx, req, filterModelPullEntries(catalog.Models, req.VariantIDs)), nil
}

// PullFormationModels pulls the sovereign ExecutionTopologies served tags through
// the governed Inference Operator.
func PullFormationModels(ctx context.Context, req ModelPullRequest) (*ModelPullResult, error) {
	if err := validateModelPullRequest(req); err != nil {
		return nil, err
	}
	entries, err := FormationCatalogPullEntries()
	if err != nil {
		return nil, fmt.Errorf("evaluation: pull formation models: %w", err)
	}
	return pullModels(ctx, req, filterModelPullEntries(entries, req.VariantIDs)), nil
}

func pullModels(ctx context.Context, req ModelPullRequest, entries []ModelPullEntry) *ModelPullResult {
	maintenance := OllamaModelMaintenanceContext{
		TargetOperatorSessionID: req.InferenceSessionID,
		Timeout:                 req.Timeout,
		CaseID:                  req.CaseID,
		NewID:                   req.NewID,
	}
	result := &ModelPullResult{}
	for _, entry := range entries {
		switch entry.Staging.Method {
		case "ollama_hf_pull", "ollama_library_pull":
			if err := pullModelEntry(ctx, req, maintenance, entry, result); err != nil {
				result.Failed = append(result.Failed, ModelPullFailure{
					VariantID:      entry.VariantID,
					ServedModelTag: entry.ServedModelTag,
					Err:            err.Error(),
					Cause:          err,
				})
				emitModelPullEvent(req, ModelPullEvent{
					VariantID:      entry.VariantID,
					ServedModelTag: entry.ServedModelTag,
					Action:         ModelPullActionPull,
					Status:         ModelPullStatusFailed,
					Detail:         err.Error(),
				})
			}
		case "manual_create":
			reason := strings.TrimSpace(entry.Staging.OllamaPullError)
			if reason == "" {
				reason = "requires manual ollama create from Hugging Face GGUF"
			}
			skipModelPullEntry(req, entry, reason, result)
		default:
			reason := strings.TrimSpace(entry.Staging.Status)
			if reason == "" {
				reason = "staging method not supported yet"
			}
			skipModelPullEntry(req, entry, reason, result)
		}
	}
	return result
}

func skipModelPullEntry(req ModelPullRequest, entry ModelPullEntry, reason string, result *ModelPullResult) {
	result.Skipped = append(result.Skipped, ModelPullSkip{
		VariantID:      entry.VariantID,
		ServedModelTag: entry.ServedModelTag,
		Reason:         reason,
	})
	emitModelPullEvent(req, ModelPullEvent{
		VariantID:      entry.VariantID,
		ServedModelTag: entry.ServedModelTag,
		Action:         ModelPullActionSkip,
		Status:         entry.Staging.Method,
		Detail:         reason,
	})
}

func pullModelEntry(ctx context.Context, req ModelPullRequest, maintenance OllamaModelMaintenanceContext, entry ModelPullEntry, result *ModelPullResult) error {
	pullTag := strings.TrimSpace(entry.Staging.OllamaPull)
	if pullTag == "" {
		return fmt.Errorf("evaluation: pull models: missing ollama_pull for %q: %w", entry.VariantID, constants.ErrMissingRequiredField)
	}
	alias := strings.TrimSpace(entry.Staging.Alias)
	if alias == "" {
		alias = entry.ServedModelTag
	}

	emitModelPullEvent(req, ModelPullEvent{
		VariantID:      entry.VariantID,
		ServedModelTag: entry.ServedModelTag,
		Action:         ModelPullActionPull,
		Status:         ModelPullStatusStart,
		Detail:         pullTag,
	})
	if req.DryRun {
		result.Skipped = append(result.Skipped, ModelPullSkip{
			VariantID:      entry.VariantID,
			ServedModelTag: entry.ServedModelTag,
			Reason:         "dry-run",
		})
		return nil
	}

	if err := PullOllamaModel(ctx, req.Dispatcher, maintenance, pullTag); err != nil {
		return fmt.Errorf("pull %s: %w", pullTag, err)
	}
	emitModelPullEvent(req, ModelPullEvent{
		VariantID:      entry.VariantID,
		ServedModelTag: entry.ServedModelTag,
		Action:         ModelPullActionPull,
		Status:         ModelPullStatusSuccess,
		Detail:         pullTag,
	})
	result.Pulled = append(result.Pulled, pullTag)

	if alias != "" && alias != pullTag {
		emitModelPullEvent(req, ModelPullEvent{
			VariantID:      entry.VariantID,
			ServedModelTag: entry.ServedModelTag,
			Action:         ModelPullActionAlias,
			Status:         ModelPullStatusStart,
			Detail:         alias,
		})
		if err := CopyOllamaModel(ctx, req.Dispatcher, maintenance, pullTag, alias); err != nil {
			return fmt.Errorf("alias %s -> %s: %w", pullTag, alias, err)
		}
		result.Aliased = append(result.Aliased, alias)
		emitModelPullEvent(req, ModelPullEvent{
			VariantID:      entry.VariantID,
			ServedModelTag: entry.ServedModelTag,
			Action:         ModelPullActionAlias,
			Status:         ModelPullStatusSuccess,
			Detail:         alias,
		})
	}
	return nil
}

func filterModelPullEntries(entries []ModelPullEntry, variantIDs []string) []ModelPullEntry {
	if len(variantIDs) == 0 {
		return append([]ModelPullEntry(nil), entries...)
	}
	want := make(map[string]struct{}, len(variantIDs))
	for _, id := range variantIDs {
		id = strings.TrimSpace(id)
		if id != "" {
			want[id] = struct{}{}
		}
	}
	selected := make([]ModelPullEntry, 0, len(want))
	for _, entry := range entries {
		if _, ok := want[entry.VariantID]; ok {
			selected = append(selected, entry)
		}
	}
	return selected
}

func emitModelPullEvent(req ModelPullRequest, event ModelPullEvent) {
	if req.Progress != nil {
		req.Progress(event)
	}
}

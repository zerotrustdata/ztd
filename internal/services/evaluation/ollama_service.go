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
	"net/http"
	"slices"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/inference"
	"github.com/zerotrustdata/ztd/internal/services/operatorcapability"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// OllamaModelCommandDispatchRequest carries one governed, unscored model
// maintenance command to the exact Inference Operator session.
type OllamaModelCommandDispatchRequest struct {
	TargetOperatorSessionID string
	ActionType              constants.ActionType
	Command                 string
	Payload                 []byte
	Environment             map[string]string
	WorkingDirectory        string
	TimeoutSeconds          int32
	ExecutionID             string
	CaseID                  string
	InvestigationID         string
	TaskID                  string
}

// OllamaModelCommandDispatchResult is the Operator outcome for one model
// maintenance command.
type OllamaModelCommandDispatchResult struct {
	Status          int
	Success         bool
	ActionType      constants.ActionType
	CommandResult   *operatorv1.CommandResult
	InventoryResult *operatorv1.OllamaModelInventoryResult
	ResidencyResult *operatorv1.OllamaModelResidencyResult
	ReleaseResult   *operatorv1.OllamaModelReleaseResult
	ResponseBody    []byte
}

// OllamaModelCommandDispatcher sends governed model maintenance commands to
// one exact Operator session.
type OllamaModelCommandDispatcher interface {
	DispatchOllamaModelCommand(context.Context, OllamaModelCommandDispatchRequest) (*OllamaModelCommandDispatchResult, error)
}

// ValidateOllamaModelCommandResult validates the HTTP and Operator result for
// one EXECUTE_BASH model maintenance command.
func ValidateOllamaModelCommandResult(command string, result *OllamaModelCommandDispatchResult) error {
	if result == nil {
		return fmt.Errorf("%w: Ollama model command %q returned no result", constants.ErrEvaluationDispatchFailed, command)
	}
	if result.Status != http.StatusOK || !result.Success {
		return fmt.Errorf("%w: Ollama model command %q rejected with status %d", constants.ErrEvaluationDispatchFailed, command, result.Status)
	}
	if result.CommandResult == nil {
		return fmt.Errorf("%w: Ollama model command %q missing command result", constants.ErrEvaluationDispatchFailed, command)
	}
	if result.CommandResult.GetStatus() != operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED {
		return fmt.Errorf("%w: Ollama model command %q failed: %s", constants.ErrEvaluationDispatchFailed, command, result.CommandResult.GetError())
	}
	if result.CommandResult.GetReturnCode() != 0 {
		if cause := lastOutputLine(result.CommandResult.GetStderr()); cause != "" {
			return fmt.Errorf("%w: Ollama model command %q exited with code %d: %s", constants.ErrEvaluationDispatchFailed, command, result.CommandResult.GetReturnCode(), cause)
		}
		return fmt.Errorf("%w: Ollama model command %q exited with code %d", constants.ErrEvaluationDispatchFailed, command, result.CommandResult.GetReturnCode())
	}
	return nil
}

// lastOutputLine returns the final non-empty line of command output, where the
// ztd CLI writes the error that ended the command.
func lastOutputLine(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// ValidateOllamaModelInventoryResult validates the HTTP and Operator result
// for one typed inventory query.
func ValidateOllamaModelInventoryResult(result *OllamaModelCommandDispatchResult) error {
	if result == nil {
		return fmt.Errorf("%w: Ollama model inventory returned no result", constants.ErrEvaluationDispatchFailed)
	}
	if result.Status != http.StatusOK || !result.Success {
		return fmt.Errorf("%w: Ollama model inventory rejected with status %d", constants.ErrEvaluationDispatchFailed, result.Status)
	}
	if result.InventoryResult == nil {
		return fmt.Errorf("%w: Ollama model inventory missing typed result", constants.ErrEvaluationDispatchFailed)
	}
	if result.InventoryResult.GetStatus() != operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED {
		return fmt.Errorf("%w: Ollama model inventory failed: %s", constants.ErrEvaluationDispatchFailed, result.InventoryResult.GetErrorMessage())
	}
	if len(result.InventoryResult.GetEntries()) == 0 {
		return fmt.Errorf("%w: Ollama model inventory returned no entries", constants.ErrInferenceModelNotFound)
	}
	return nil
}

// ValidateOllamaModelResidencyResult validates the HTTP and Operator result for
// one typed residency query.
func ValidateOllamaModelResidencyResult(result *OllamaModelCommandDispatchResult) error {
	if result == nil {
		return fmt.Errorf("%w: Ollama model residency returned no result", constants.ErrEvaluationDispatchFailed)
	}
	if result.Status != http.StatusOK || !result.Success {
		return fmt.Errorf("%w: Ollama model residency rejected with status %d", constants.ErrEvaluationDispatchFailed, result.Status)
	}
	if result.ResidencyResult == nil {
		return fmt.Errorf("%w: Ollama model residency missing typed result", constants.ErrEvaluationDispatchFailed)
	}
	if result.ResidencyResult.GetStatus() != operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED {
		return fmt.Errorf("%w: Ollama model residency failed: %s", constants.ErrEvaluationDispatchFailed, result.ResidencyResult.GetErrorMessage())
	}
	return nil
}

// ValidateOllamaModelReleaseResult validates the HTTP and Operator result for
// one typed batch release.
func ValidateOllamaModelReleaseResult(result *OllamaModelCommandDispatchResult) error {
	if result == nil {
		return fmt.Errorf("%w: Ollama model release returned no result", constants.ErrEvaluationDispatchFailed)
	}
	if result.Status != http.StatusOK || !result.Success {
		return fmt.Errorf("%w: Ollama model release rejected with status %d", constants.ErrEvaluationDispatchFailed, result.Status)
	}
	if result.ReleaseResult == nil {
		return fmt.Errorf("%w: Ollama model release missing typed result", constants.ErrEvaluationDispatchFailed)
	}
	if result.ReleaseResult.GetStatus() != operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED {
		return fmt.Errorf("%w: Ollama model release failed: %s", constants.ErrEvaluationDispatchFailed, result.ReleaseResult.GetErrorMessage())
	}
	return nil
}

// MarshalOllamaModelCommandPayload builds the typed EXECUTE_BASH payload for
// one unscored model maintenance command.
func MarshalOllamaModelCommandPayload(request OllamaModelCommandDispatchRequest) ([]byte, error) {
	if request.Command == "" || request.ExecutionID == "" {
		return nil, fmt.Errorf("%w: model command and execution ID are required", constants.ErrMissingRequiredField)
	}
	payload, err := proto.Marshal(&operatorv1.CommandRequested{
		Command:          request.Command,
		ExecutionId:      request.ExecutionID,
		Justification:    "evaluation campaign model maintenance",
		Environment:      request.Environment,
		WorkingDirectory: request.WorkingDirectory,
		TimeoutSeconds:   request.TimeoutSeconds,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: marshal Ollama model command: %v", constants.ErrEvaluationDispatchFailed, err)
	}
	return payload, nil
}

// MarshalOllamaModelInventoryPayload builds the typed inventory query payload.
func MarshalOllamaModelInventoryPayload(executionID string) ([]byte, error) {
	if executionID == "" {
		return nil, fmt.Errorf("%w: execution ID is required", constants.ErrMissingRequiredField)
	}
	payload, err := proto.Marshal(&operatorv1.OllamaModelInventoryRequested{ExecutionId: executionID})
	if err != nil {
		return nil, fmt.Errorf("%w: marshal Ollama model inventory request: %v", constants.ErrEvaluationDispatchFailed, err)
	}
	return payload, nil
}

// MarshalOllamaModelResidencyPayload builds the typed residency query payload.
func MarshalOllamaModelResidencyPayload(executionID string) ([]byte, error) {
	if executionID == "" {
		return nil, fmt.Errorf("%w: execution ID is required", constants.ErrMissingRequiredField)
	}
	payload, err := proto.Marshal(&operatorv1.OllamaModelResidencyRequested{ExecutionId: executionID})
	if err != nil {
		return nil, fmt.Errorf("%w: marshal Ollama model residency request: %v", constants.ErrEvaluationDispatchFailed, err)
	}
	return payload, nil
}

// MarshalOllamaModelReleasePayload builds the typed batch release payload.
func MarshalOllamaModelReleasePayload(executionID string, modelTags []string) ([]byte, error) {
	if executionID == "" || len(modelTags) == 0 {
		return nil, fmt.Errorf("%w: execution ID and model tags are required", constants.ErrMissingRequiredField)
	}
	payload, err := proto.Marshal(&operatorv1.OllamaModelReleaseRequested{ExecutionId: executionID, ModelTags: modelTags})
	if err != nil {
		return nil, fmt.Errorf("%w: marshal Ollama model release request: %v", constants.ErrEvaluationDispatchFailed, err)
	}
	return payload, nil
}

// OllamaModelMaintenanceContext carries governed model-maintenance dispatch
// fields shared by pull, copy, inventory, and residency commands.
type OllamaModelMaintenanceContext struct {
	TargetOperatorSessionID string
	Environment             map[string]string
	Timeout                 time.Duration
	CaseID                  string
	NewID                   func(string) string
}

func (c OllamaModelMaintenanceContext) validate() error {
	if c.TargetOperatorSessionID == "" || c.NewID == nil {
		return fmt.Errorf("evaluation: Ollama model maintenance: %w", constants.ErrMissingRequiredField)
	}
	return nil
}

func (c OllamaModelMaintenanceContext) timeoutSeconds() int32 {
	if c.Timeout <= 0 {
		return int32(inference.ProviderRequestTimeout / time.Second)
	}
	seconds := int32(c.Timeout / time.Second)
	if seconds <= 0 {
		return 1
	}
	return seconds
}

func dispatchOllamaBashCommand(
	ctx context.Context,
	dispatcher OllamaModelCommandDispatcher,
	maintenance OllamaModelMaintenanceContext,
	command string,
	investigationID string,
	executionPrefix string,
) error {
	if dispatcher == nil {
		return fmt.Errorf("evaluation: Ollama model maintenance: %w", constants.ErrMissingRequiredField)
	}
	if err := maintenance.validate(); err != nil {
		return err
	}
	caseID := strings.TrimSpace(maintenance.CaseID)
	if caseID == "" {
		caseID = constants.ProviderModelMaintenanceCaseID
	}
	executionID := maintenance.NewID(executionPrefix)
	payload, err := MarshalOllamaModelCommandPayload(OllamaModelCommandDispatchRequest{
		Command:        command,
		ExecutionID:    executionID,
		Environment:    maintenance.Environment,
		TimeoutSeconds: maintenance.timeoutSeconds(),
	})
	if err != nil {
		return err
	}
	request := OllamaModelCommandDispatchRequest{
		TargetOperatorSessionID: maintenance.TargetOperatorSessionID,
		ActionType:              constants.ActionTypeExecuteBash,
		Command:                 command,
		Payload:                 payload,
		Environment:             maintenance.Environment,
		TimeoutSeconds:          maintenance.timeoutSeconds(),
		ExecutionID:             executionID,
		CaseID:                  caseID,
		InvestigationID:         investigationID,
		TaskID:                  maintenance.NewID(executionPrefix + "-task"),
	}
	result, err := dispatcher.DispatchOllamaModelCommand(ctx, request)
	if err != nil {
		return fmt.Errorf("evaluation: Ollama model maintenance: %w", err)
	}
	if err := ValidateOllamaModelCommandResult(command, result); err != nil {
		return fmt.Errorf("evaluation: Ollama model maintenance: %w", err)
	}
	return nil
}

func dispatchOllamaTypedQuery(
	ctx context.Context,
	dispatcher OllamaModelCommandDispatcher,
	maintenance OllamaModelMaintenanceContext,
	actionType constants.ActionType,
	payload []byte,
	investigationID string,
	executionID string,
	validate func(*OllamaModelCommandDispatchResult) error,
) (*OllamaModelCommandDispatchResult, error) {
	if dispatcher == nil {
		return nil, fmt.Errorf("evaluation: Ollama model maintenance: %w", constants.ErrMissingRequiredField)
	}
	if err := maintenance.validate(); err != nil {
		return nil, err
	}
	caseID := strings.TrimSpace(maintenance.CaseID)
	if caseID == "" {
		caseID = constants.ProviderModelMaintenanceCaseID
	}
	request := OllamaModelCommandDispatchRequest{
		TargetOperatorSessionID: maintenance.TargetOperatorSessionID,
		ActionType:              actionType,
		Payload:                 payload,
		Environment:             maintenance.Environment,
		TimeoutSeconds:          maintenance.timeoutSeconds(),
		ExecutionID:             executionID,
		CaseID:                  caseID,
		InvestigationID:         investigationID,
		TaskID:                  maintenance.NewID(executionID + "-task"),
	}
	result, err := dispatcher.DispatchOllamaModelCommand(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("evaluation: Ollama model maintenance: %w", err)
	}
	if err := validate(result); err != nil {
		return nil, fmt.Errorf("evaluation: Ollama model maintenance: %w", err)
	}
	return result, nil
}

// PullOllamaModel downloads one model tag through the exact Inference Operator.
func PullOllamaModel(ctx context.Context, dispatcher OllamaModelCommandDispatcher, maintenance OllamaModelMaintenanceContext, modelTag string) error {
	command, err := operatorcapability.OllamaPullCommand(modelTag)
	if err != nil {
		return fmt.Errorf("evaluation: pull Ollama model: %w", err)
	}
	err = dispatchOllamaBashCommand(ctx, dispatcher, maintenance, command, constants.ProviderModelPullInvestigationID, "ollama-pull")
	return err
}

// CopyOllamaModel aliases one model tag through the exact Inference Operator.
func CopyOllamaModel(ctx context.Context, dispatcher OllamaModelCommandDispatcher, maintenance OllamaModelMaintenanceContext, source, destination string) error {
	command, err := operatorcapability.OllamaCopyCommand(source, destination)
	if err != nil {
		return fmt.Errorf("evaluation: copy Ollama model: %w", err)
	}
	err = dispatchOllamaBashCommand(ctx, dispatcher, maintenance, command, constants.ProviderModelCopyInvestigationID, "ollama-copy")
	return err
}

// ListOllamaProviderInventory queries provider inventory through the exact
// Inference Operator session.
func ListOllamaProviderInventory(ctx context.Context, dispatcher OllamaModelCommandDispatcher, maintenance OllamaModelMaintenanceContext) ([]inference.ProviderModelInventoryEntry, error) {
	executionID := maintenance.NewID("ollama-inventory")
	payload, err := MarshalOllamaModelInventoryPayload(executionID)
	if err != nil {
		return nil, err
	}
	result, err := dispatchOllamaTypedQuery(
		ctx,
		dispatcher,
		maintenance,
		constants.ActionTypeOllamaModelInventory,
		payload,
		constants.ProviderModelInventoryInvestigationID,
		executionID,
		ValidateOllamaModelInventoryResult,
	)
	if err != nil {
		return nil, err
	}
	return inference.ProviderModelInventoryEntriesFromProto(result.InventoryResult.GetEntries()), nil
}

// ModelResidencyRelease is one provider residency release: every model
// resident on the Inference Operator's provider outside Retain is unloaded.
type ModelResidencyRelease struct {
	// RunID is the case that scopes the residency read and release receipts.
	RunID string
	// Retain lists the served model tags that stay resident. An empty Retain
	// releases every resident model.
	Retain []string
}

// ModelResidencyReleaser releases provider model residency, so the provider
// holds only the models the next phase of a run dispatches.
type ModelResidencyReleaser interface {
	ReleaseAllExcept(ctx context.Context, release ModelResidencyRelease) ([]string, error)
}

// ProviderModelResidency releases model residency on one Inference Operator
// session through governed typed residency and release commands, which the
// Operator serves from its configured provider.
type ProviderModelResidency struct {
	dispatcher              OllamaModelCommandDispatcher
	targetOperatorSessionID string
	newID                   func(string) string
}

// NewProviderModelResidency builds the residency releaser for the Inference
// Operator session targetOperatorSessionID.
func NewProviderModelResidency(dispatcher OllamaModelCommandDispatcher, targetOperatorSessionID string, newID func(string) string) (*ProviderModelResidency, error) {
	if dispatcher == nil || targetOperatorSessionID == "" || newID == nil {
		return nil, fmt.Errorf("evaluation: provider model residency: %w", constants.ErrMissingRequiredField)
	}
	return &ProviderModelResidency{dispatcher: dispatcher, targetOperatorSessionID: targetOperatorSessionID, newID: newID}, nil
}

// ReleaseAllExcept reads the provider's residency and sends one governed batch
// release for every resident model outside release.Retain. The Operator
// validates the full set, unloads it, and verifies absence before the release
// completes. It returns the released tags; when nothing outside Retain is
// resident it dispatches no release.
func (r *ProviderModelResidency) ReleaseAllExcept(ctx context.Context, release ModelResidencyRelease) ([]string, error) {
	if release.RunID == "" {
		return nil, fmt.Errorf("evaluation: release provider models: %w", constants.ErrMissingRequiredField)
	}
	maintenance := OllamaModelMaintenanceContext{TargetOperatorSessionID: r.targetOperatorSessionID, CaseID: release.RunID, NewID: r.newID}
	residency, err := readProviderResidency(ctx, r.dispatcher, maintenance)
	if err != nil {
		return nil, fmt.Errorf("evaluation: release provider models: %w", err)
	}
	var modelTags []string
	for _, name := range residency.Names() {
		if !slices.Contains(release.Retain, name) {
			modelTags = append(modelTags, name)
		}
	}
	if len(modelTags) == 0 {
		return nil, nil
	}
	if err := releaseProviderModels(ctx, r.dispatcher, maintenance, modelTags); err != nil {
		return nil, fmt.Errorf("evaluation: release provider models: %w", err)
	}
	return modelTags, nil
}

// readProviderResidency queries typed provider residency through the exact
// Inference Operator session.
func readProviderResidency(ctx context.Context, dispatcher OllamaModelCommandDispatcher, maintenance OllamaModelMaintenanceContext) (inference.ProviderResidency, error) {
	executionID := maintenance.NewID("ollama-residency")
	payload, err := MarshalOllamaModelResidencyPayload(executionID)
	if err != nil {
		return inference.ProviderResidency{}, err
	}
	result, err := dispatchOllamaTypedQuery(
		ctx,
		dispatcher,
		maintenance,
		constants.ActionTypeOllamaModelResidency,
		payload,
		constants.ProviderModelResidencyInvestigationID,
		executionID,
		ValidateOllamaModelResidencyResult,
	)
	if err != nil {
		return inference.ProviderResidency{}, err
	}
	return inference.ProviderResidencyFromProto(result.ResidencyResult)
}

// releaseProviderModels sends one governed typed batch release of modelTags
// to the exact Inference Operator session.
func releaseProviderModels(ctx context.Context, dispatcher OllamaModelCommandDispatcher, maintenance OllamaModelMaintenanceContext, modelTags []string) error {
	for _, modelTag := range modelTags {
		if err := operatorcapability.ValidateServedModelTag(modelTag); err != nil {
			return err
		}
	}
	executionID := maintenance.NewID("ollama-release")
	payload, err := MarshalOllamaModelReleasePayload(executionID, modelTags)
	if err != nil {
		return err
	}
	_, err = dispatchOllamaTypedQuery(
		ctx,
		dispatcher,
		maintenance,
		constants.ActionTypeOllamaModelRelease,
		payload,
		constants.ProviderModelReleaseInvestigationID,
		executionID,
		ValidateOllamaModelReleaseResult,
	)
	return err
}

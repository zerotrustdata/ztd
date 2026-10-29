// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// GovernedActionTool is a model-facing tool whose call is its own typed
// governed action, not an MCP_CALL. The ingress builds that action from the
// model's arguments, so the action's L1 checks, path policy, ledger and
// receipts are the ones every other caller of the action gets.
type GovernedActionTool interface {
	NativeTool
	// ActionType is the typed governed action every call of the tool becomes.
	ActionType() constants.ActionType
	// GovernedAction decodes the model's arguments strictly and returns the
	// typed action they request under binding.
	GovernedAction(args json.RawMessage, binding GovernedActionBinding) (GovernedActionRequest, error)
}

// LookupGovernedActionTool returns the native tool registered under name when
// its call is its own typed governed action.
func LookupGovernedActionTool(name string) (GovernedActionTool, bool, error) {
	registry := NewToolRegistry()
	if err := RegisterNativeTools(registry); err != nil {
		return nil, false, err
	}
	tool, ok := registry.Get(name)
	if !ok {
		return nil, false, nil
	}
	governed, ok := tool.(GovernedActionTool)
	return governed, ok, nil
}

// GovernedActionBinding is what the platform, never the model, supplies to a
// governed action tool call.
type GovernedActionBinding struct {
	// OperatorSessionID is the verified session the action executes under.
	// File history, diff and restore read only that session's ledger.
	OperatorSessionID string
	// ExecutionID identifies the call in the action's typed payload.
	ExecutionID string
}

// GovernedActionRequest is the typed governed action one tool call becomes.
type GovernedActionRequest struct {
	ActionType     constants.ActionType
	EventType      constants.EventType
	TargetResource string
	Payload        proto.Message
}

// MarshalPayload returns the payload's deterministic protobuf encoding.
func (r GovernedActionRequest) MarshalPayload() ([]byte, error) {
	return (proto.MarshalOptions{Deterministic: true}).Marshal(r.Payload)
}

func governedActionRequest(action constants.ActionType, target string, payload proto.Message) (GovernedActionRequest, error) {
	event, err := constants.RequestEventForAction(action)
	if err != nil {
		return GovernedActionRequest{}, fmt.Errorf("mcp: governed action %s: %w", action, err)
	}
	return GovernedActionRequest{ActionType: action, EventType: event, TargetResource: target, Payload: payload}, nil
}

// decodeGovernedToolArguments decodes one JSON object into T, rejecting
// unknown fields and trailing content.
func decodeGovernedToolArguments[T any](tool string, args json.RawMessage) (T, error) {
	var value T
	raw := bytes.TrimSpace(args)
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, fmt.Errorf("%s: decode arguments: %w: %w", tool, constants.ErrGatewayInvalidToolArguments, err)
	}
	if err := decoder.Decode(new(json.RawMessage)); err != io.EOF {
		return value, fmt.Errorf("%s: decode arguments: %w: trailing content", tool, constants.ErrGatewayInvalidToolArguments)
	}
	return value, nil
}

func requireArgument(tool, name, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s: %s is required: %w", tool, name, constants.ErrMissingRequiredField)
	}
	return nil
}

func requireSession(tool string, binding GovernedActionBinding) error {
	if binding.OperatorSessionID == "" {
		return fmt.Errorf("%s: %w", tool, constants.ErrMCPToolSessionRequired)
	}
	return nil
}

// governedActionOnly is the Execute of every governed action tool: reached as
// an MCP_CALL it fails closed.
func governedActionOnly(tool string) (CallToolResult, error) {
	return CallToolResult{}, fmt.Errorf("%s: %w", tool, constants.ErrMCPToolGovernedAction)
}

// ListDirectoryTool lists one directory as FS_LIST.
type ListDirectoryTool struct{}

// ListDirectoryRequest is the params for the "list_directory" tool.
type ListDirectoryRequest struct {
	Path       string `json:"path"`
	MaxDepth   int32  `json:"max_depth,omitempty"`
	MaxEntries int32  `json:"max_entries,omitempty"`
}

func (t *ListDirectoryTool) Name() string { return constants.MCPToolListDirectory }

func (t *ListDirectoryTool) Description() string {
	return "Lists the files and subdirectories under one directory, with each entry's size and whether it is a directory."
}

func (t *ListDirectoryTool) InputSchema() *InputSchema {
	return &InputSchema{
		Type: "object",
		Properties: map[string]*PropertySchema{
			"path":        {Type: "string", Description: "Absolute or relative path of the directory to list"},
			"max_depth":   {Type: "integer", Description: fmt.Sprintf("How many directory levels to descend (optional, at most %d)", constants.FsListMaxDepth)},
			"max_entries": {Type: "integer", Description: fmt.Sprintf("Maximum entries to return (optional, at most %d)", constants.FsListMaxEntries)},
		},
		Required: []string{"path"},
	}
}

func (t *ListDirectoryTool) ActionType() constants.ActionType { return constants.ActionTypeFsList }

func (t *ListDirectoryTool) Execute(context.Context, json.RawMessage) (CallToolResult, error) {
	return governedActionOnly(t.Name())
}

func (t *ListDirectoryTool) GovernedAction(args json.RawMessage, binding GovernedActionBinding) (GovernedActionRequest, error) {
	req, err := decodeGovernedToolArguments[ListDirectoryRequest](t.Name(), args)
	if err != nil {
		return GovernedActionRequest{}, err
	}
	if err := requireArgument(t.Name(), "path", req.Path); err != nil {
		return GovernedActionRequest{}, err
	}
	return governedActionRequest(t.ActionType(), req.Path, &operatorv1.FsListRequested{
		Path:        req.Path,
		ExecutionId: binding.ExecutionID,
		MaxDepth:    req.MaxDepth,
		MaxEntries:  req.MaxEntries,
	})
}

// FileEditTool writes or edits one file as FILE_EDIT.
type FileEditTool struct{}

// FileEditToolRequest is the params for the "file_edit" tool.
type FileEditToolRequest struct {
	FilePath       string `json:"file_path"`
	Operation      string `json:"operation"`
	Justification  string `json:"justification"`
	Content        string `json:"content,omitempty"`
	OldContent     string `json:"old_content,omitempty"`
	NewContent     string `json:"new_content,omitempty"`
	InsertContent  string `json:"insert_content,omitempty"`
	InsertPosition int32  `json:"insert_position,omitempty"`
	StartLine      int32  `json:"start_line,omitempty"`
	EndLine        int32  `json:"end_line,omitempty"`
}

// fileEditToolOperations are the FILE_EDIT operations a model may request.
// Reading stays on read_file.
var fileEditToolOperations = []string{
	string(constants.FileOperationWrite),
	string(constants.FileOperationReplace),
	string(constants.FileOperationInsert),
	string(constants.FileOperationDelete),
}

func (t *FileEditTool) Name() string { return constants.MCPToolFileEdit }

func (t *FileEditTool) Description() string {
	return "Changes one file as a governed, ledgered edit. write replaces the whole file with content (creating it if missing); " +
		"replace swaps the first exact occurrence of old_content for new_content; insert adds insert_content before line insert_position; " +
		"delete removes lines start_line through end_line. Every edit is snapshotted before and after, so restore_file can undo it."
}

func (t *FileEditTool) InputSchema() *InputSchema {
	return &InputSchema{
		Type: "object",
		Properties: map[string]*PropertySchema{
			"file_path":       {Type: "string", Description: "Absolute or relative path of the file to change"},
			"operation":       {Type: "string", Description: "The kind of change", Enum: fileEditToolOperations},
			"justification":   {Type: "string", Description: "Why this change is needed, recorded with the edit"},
			"content":         {Type: "string", Description: "write: the complete new file content"},
			"old_content":     {Type: "string", Description: "replace: the exact text to replace"},
			"new_content":     {Type: "string", Description: "replace: the replacement text"},
			"insert_content":  {Type: "string", Description: "insert: the text to insert"},
			"insert_position": {Type: "integer", Description: "insert: the 1-indexed line to insert before"},
			"start_line":      {Type: "integer", Description: "delete: the first 1-indexed line to remove"},
			"end_line":        {Type: "integer", Description: "delete: the last 1-indexed line to remove"},
		},
		Required: []string{"file_path", "operation", "justification"},
	}
}

func (t *FileEditTool) ActionType() constants.ActionType { return constants.ActionTypeFileEdit }

func (t *FileEditTool) Execute(context.Context, json.RawMessage) (CallToolResult, error) {
	return governedActionOnly(t.Name())
}

func (t *FileEditTool) GovernedAction(args json.RawMessage, binding GovernedActionBinding) (GovernedActionRequest, error) {
	req, err := decodeGovernedToolArguments[FileEditToolRequest](t.Name(), args)
	if err != nil {
		return GovernedActionRequest{}, err
	}
	for name, value := range map[string]string{"file_path": req.FilePath, "operation": req.Operation, "justification": req.Justification} {
		if err := requireArgument(t.Name(), name, value); err != nil {
			return GovernedActionRequest{}, err
		}
	}
	operation := constants.FileOperation(req.Operation)
	switch operation {
	case constants.FileOperationWrite, constants.FileOperationReplace, constants.FileOperationInsert, constants.FileOperationDelete:
	default:
		return GovernedActionRequest{}, fmt.Errorf("%s: operation %q: %w", t.Name(), req.Operation, constants.ErrFileEditUnsupportedOperation)
	}
	return governedActionRequest(t.ActionType(), req.FilePath, &operatorv1.FileEditRequested{
		FilePath:        req.FilePath,
		Operation:       string(operation),
		ExecutionId:     binding.ExecutionID,
		Justification:   req.Justification,
		Content:         req.Content,
		OldContent:      req.OldContent,
		NewContent:      req.NewContent,
		InsertContent:   req.InsertContent,
		InsertPosition:  req.InsertPosition,
		StartLine:       req.StartLine,
		EndLine:         req.EndLine,
		CreateIfMissing: operation == constants.FileOperationWrite,
	})
}

// FileHistoryTool lists one file's ledger commits as FETCH_FILE_HISTORY.
type FileHistoryTool struct{}

// FileHistoryRequest is the params for the "file_history" tool.
type FileHistoryRequest struct {
	FilePath string `json:"file_path"`
	Limit    int32  `json:"limit,omitempty"`
}

func (t *FileHistoryTool) Name() string { return constants.MCPToolFileHistory }

func (t *FileHistoryTool) Description() string {
	return "Lists the ledger commits recorded for one file in this session, newest first, with each commit_hash, timestamp and message. " +
		"Pass a commit_hash to restore_file to return the file to that version."
}

func (t *FileHistoryTool) InputSchema() *InputSchema {
	return &InputSchema{
		Type: "object",
		Properties: map[string]*PropertySchema{
			"file_path": {Type: "string", Description: "Absolute or relative path of the file"},
			"limit":     {Type: "integer", Description: "Maximum commits to return (optional, default 50)"},
		},
		Required: []string{"file_path"},
	}
}

func (t *FileHistoryTool) ActionType() constants.ActionType {
	return constants.ActionTypeFetchFileHistory
}

func (t *FileHistoryTool) Execute(context.Context, json.RawMessage) (CallToolResult, error) {
	return governedActionOnly(t.Name())
}

func (t *FileHistoryTool) GovernedAction(args json.RawMessage, binding GovernedActionBinding) (GovernedActionRequest, error) {
	req, err := decodeGovernedToolArguments[FileHistoryRequest](t.Name(), args)
	if err != nil {
		return GovernedActionRequest{}, err
	}
	if err := requireArgument(t.Name(), "file_path", req.FilePath); err != nil {
		return GovernedActionRequest{}, err
	}
	if err := requireSession(t.Name(), binding); err != nil {
		return GovernedActionRequest{}, err
	}
	return governedActionRequest(t.ActionType(), req.FilePath, &operatorv1.FetchFileHistoryRequested{
		ExecutionId:       binding.ExecutionID,
		FilePath:          req.FilePath,
		Limit:             req.Limit,
		OperatorSessionId: binding.OperatorSessionID,
	})
}

// FileDiffTool lists one file's recorded mutations, or returns one mutation's
// diff, as FETCH_FILE_DIFF.
type FileDiffTool struct{}

// FileDiffRequest is the params for the "file_diff" tool.
type FileDiffRequest struct {
	FilePath string `json:"file_path,omitempty"`
	DiffID   string `json:"diff_id,omitempty"`
	Limit    int32  `json:"limit,omitempty"`
}

func (t *FileDiffTool) Name() string { return constants.MCPToolFileDiff }

func (t *FileDiffTool) Description() string {
	return "With file_path, lists the governed changes recorded for that file in this session: each change's id, operation, timestamp, diff_stat, " +
		"and the ledger_hash_before and ledger_hash_after commits around it. With diff_id, returns that one change's unified diff. " +
		"To undo a change, pass its ledger_hash_before to restore_file."
}

func (t *FileDiffTool) InputSchema() *InputSchema {
	return &InputSchema{
		Type: "object",
		Properties: map[string]*PropertySchema{
			"file_path": {Type: "string", Description: "List the recorded changes of this file"},
			"diff_id":   {Type: "string", Description: "Return the diff of this recorded change (an id from a file_path listing)"},
			"limit":     {Type: "integer", Description: "Maximum changes to list (optional, default 50)"},
		},
	}
}

func (t *FileDiffTool) ActionType() constants.ActionType { return constants.ActionTypeFetchFileDiff }

func (t *FileDiffTool) Execute(context.Context, json.RawMessage) (CallToolResult, error) {
	return governedActionOnly(t.Name())
}

func (t *FileDiffTool) GovernedAction(args json.RawMessage, binding GovernedActionBinding) (GovernedActionRequest, error) {
	req, err := decodeGovernedToolArguments[FileDiffRequest](t.Name(), args)
	if err != nil {
		return GovernedActionRequest{}, err
	}
	if strings.TrimSpace(req.FilePath) == "" && strings.TrimSpace(req.DiffID) == "" {
		return GovernedActionRequest{}, fmt.Errorf("%s: file_path or diff_id is required: %w", t.Name(), constants.ErrMissingRequiredField)
	}
	if err := requireSession(t.Name(), binding); err != nil {
		return GovernedActionRequest{}, err
	}
	target := req.FilePath
	if target == "" {
		target = req.DiffID
	}
	return governedActionRequest(t.ActionType(), target, &operatorv1.FetchFileDiffRequested{
		ExecutionId:       binding.ExecutionID,
		DiffId:            req.DiffID,
		OperatorSessionId: binding.OperatorSessionID,
		FilePath:          req.FilePath,
		Limit:             req.Limit,
	})
}

// RestoreFileTool restores one file from a ledger commit as RESTORE_FILE.
type RestoreFileTool struct{}

// RestoreFileToolRequest is the params for the "restore_file" tool.
type RestoreFileToolRequest struct {
	FilePath   string `json:"file_path"`
	CommitHash string `json:"commit_hash"`
}

func (t *RestoreFileTool) Name() string { return constants.MCPToolRestoreFile }

func (t *RestoreFileTool) Description() string {
	return "Restores one file to its content at a ledger commit of this session, as a governed, ledgered change. " +
		"Get the commit_hash from file_history, or from the ledger_hash_before of the change to undo in file_diff."
}

func (t *RestoreFileTool) InputSchema() *InputSchema {
	return &InputSchema{
		Type: "object",
		Properties: map[string]*PropertySchema{
			"file_path":   {Type: "string", Description: "Absolute or relative path of the file to restore"},
			"commit_hash": {Type: "string", Description: "The ledger commit to restore the file to"},
		},
		Required: []string{"file_path", "commit_hash"},
	}
}

func (t *RestoreFileTool) ActionType() constants.ActionType { return constants.ActionTypeRestoreFile }

func (t *RestoreFileTool) Execute(context.Context, json.RawMessage) (CallToolResult, error) {
	return governedActionOnly(t.Name())
}

func (t *RestoreFileTool) GovernedAction(args json.RawMessage, binding GovernedActionBinding) (GovernedActionRequest, error) {
	req, err := decodeGovernedToolArguments[RestoreFileToolRequest](t.Name(), args)
	if err != nil {
		return GovernedActionRequest{}, err
	}
	if err := requireArgument(t.Name(), "file_path", req.FilePath); err != nil {
		return GovernedActionRequest{}, err
	}
	if err := requireArgument(t.Name(), "commit_hash", req.CommitHash); err != nil {
		return GovernedActionRequest{}, err
	}
	if err := requireSession(t.Name(), binding); err != nil {
		return GovernedActionRequest{}, err
	}
	return governedActionRequest(t.ActionType(), req.FilePath, &operatorv1.RestoreFileRequested{
		ExecutionId:       binding.ExecutionID,
		FilePath:          req.FilePath,
		CommitHash:        req.CommitHash,
		OperatorSessionId: binding.OperatorSessionID,
	})
}

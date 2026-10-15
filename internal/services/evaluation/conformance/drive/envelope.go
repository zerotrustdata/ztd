// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.
package drive

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/cli/operator"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// baselineTTL is how long a baseline envelope stays admissible.
const baselineTTL = 5 * time.Minute

// ungovernedCollection names a document collection that is not in the governed
// set, so DOCUMENT_UPDATE on it is refused at L4 (row 15).
const ungovernedCollection = "conformance_ungoverned"

// Identity is who submits and which Operator session executes.
type Identity struct {
	Persona client.Persona
	Target  operator.Target // embedded: OperatorID == constants.DocIDEmbeddedOperator
}

// Baseline builds a valid, hashed, unsigned-by-L2 envelope for one attempt.
// stateRoot comes from client.StateRoot at build time.
func Baseline(a cases.Attempt, id Identity, stateRoot string, now time.Time) (*commonv1.GovernanceEnvelope, error) {
	action, event, msg, err := Payload(a)
	if err != nil {
		return nil, fmt.Errorf("baseline %s: %w", a.Case.ID, err)
	}
	payload, err := proto.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("baseline %s: marshal payload: %w", a.Case.ID, err)
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("baseline %s: nonce: %w", a.Case.ID, err)
	}

	env := &commonv1.GovernanceEnvelope{
		ProtocolVersion:   client.ProtocolVersion,
		Timestamp:         timestamppb.New(now),
		ExpiresAt:         timestamppb.New(now.Add(baselineTTL)),
		SourceComponent:   commonv1.Component_COMPONENT_CLIENT,
		OperatorId:        id.Target.OperatorID,
		OperatorSessionId: id.Target.OperatorSessionID,
		CliSessionId:      id.Persona.CLISessionID,
		RequestorUserId:   id.Persona.UserID,
		ActionType:        string(action),
		EventType:         string(event),
		TargetResource:    targetResource(a, action),
		Payload:           payload,
		StateMerkleRoot:   stateRoot,
		Nonce:             hex.EncodeToString(nonce),
	}
	if err := Rehash(env); err != nil {
		return nil, fmt.Errorf("baseline %s: %w", a.Case.ID, err)
	}
	return env, nil
}

// Rehash recomputes transaction_hash and id with governance.GenerateMessageID.
func Rehash(env *commonv1.GovernanceEnvelope) error {
	hash, err := governance.GenerateMessageID(env)
	if err != nil {
		return fmt.Errorf("rehash: %w", err)
	}
	env.Id = hash
	env.TransactionHash = hash
	return nil
}

// Payload returns the action, event and payload message for an attempt. Baseline
// and the dispatch driver (T16) both call it.
func Payload(a cases.Attempt) (constants.ActionType, constants.EventType, proto.Message, error) {
	switch {
	case a.Case.Fault == cases.FaultDoctrine:
		if a.Case.Command == "" {
			return "", "", nil, fmt.Errorf("payload %s: doctrine command: %w", a.Case.ID, constants.ErrMissingRequiredField)
		}
		return constants.ActionTypeExecuteBash, constants.EventOperatorCommandRequested,
			&operatorv1.CommandRequested{Command: a.Case.Command, ExecutionId: a.AttemptID}, nil
	case a.Case.Fault == cases.FaultUngovernedCollection:
		return constants.ActionTypeDocumentUpdate, constants.EventAppDocumentUpdateRequested,
			&operatorv1.DocumentUpdateRequested{Collection: ungovernedCollection, DocumentId: a.AttemptID}, nil
	case a.Case.Action == cases.ActionMutation:
		return constants.ActionTypeFileEdit, constants.EventOperatorFileEditRequested,
			&operatorv1.FileEditRequested{
				FilePath:        a.TargetAbs,
				Operation:       string(constants.FileOperationWrite),
				Content:         a.Marker,
				ExecutionId:     a.AttemptID,
				CreateIfMissing: true,
			}, nil
	case a.Case.Action == cases.ActionRead:
		return constants.ActionTypeFsRead, constants.EventOperatorFilesystemReadRequested,
			&operatorv1.FsReadRequested{Path: a.ReadAbs, ExecutionId: a.AttemptID}, nil
	default:
		return "", "", nil, fmt.Errorf("payload %s: action %q: %w", a.Case.ID, a.Case.Action, constants.ErrConformanceIngressUnsupported)
	}
}

// targetResource is the path or document the action names, as the platform lane
// sets it for file edits and reads.
func targetResource(a cases.Attempt, action constants.ActionType) string {
	switch action {
	case constants.ActionTypeFsRead:
		return a.ReadAbs
	case constants.ActionTypeDocumentUpdate:
		return ungovernedCollection + "/" + a.AttemptID
	default:
		return a.TargetAbs
	}
}

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"fmt"

	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle"
	"github.com/zerotrustdata/ztd/internal/services/operatorcapability"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// CampaignOperatorRuntime is the executing Operator runtime one campaign role
// resolves to. OperatorID names the runtime: its signing key and its
// commitment ledger. Anchor pins that runtime's actuator key as bound at
// enrollment; it is nil until WithAnchors binds it.
type CampaignOperatorRuntime struct {
	OperatorID        string
	OperatorSessionID string
	Anchor            *evalv1.ConformanceAnchor
}

// CampaignOperatorTopology is the set of Operator runtimes a campaign executes
// against: the Inference role serving model turns and the Data role executing
// tool calls. One Operator may hold both capabilities (the Gateway's embedded
// Operator defaults to data,inference); the roles then share one runtime, so
// one commitment ledger records both roles' executions and evidence must claim
// both. DataWorkingDirectory is the Data runtime's working directory, the root
// of every scenario workspace.
type CampaignOperatorTopology struct {
	Inference            CampaignOperatorRuntime
	Data                 CampaignOperatorRuntime
	DataWorkingDirectory string
}

// ResolveCampaignOperatorTopology resolves the Inference and Data roles from the
// operator registry. inferenceSessionID pins the Inference session when set;
// otherwise SelectInferenceOperator's default applies. Anchors are not bound.
func ResolveCampaignOperatorTopology(operators []*operatorv1.OperatorDocument, inferenceSessionID string) (CampaignOperatorTopology, error) {
	inference, err := SelectInferenceOperator(operators, inferenceSessionID)
	if err != nil {
		return CampaignOperatorTopology{}, fmt.Errorf("evaluation: resolve operator topology: %w", err)
	}
	data, err := operatorcapability.SelectDataOperator(operators)
	if err != nil {
		return CampaignOperatorTopology{}, fmt.Errorf("evaluation: resolve operator topology: %w", err)
	}
	return CampaignOperatorTopology{
		Inference:            CampaignOperatorRuntime{OperatorID: inference.OperatorID, OperatorSessionID: inference.OperatorSessionID},
		Data:                 CampaignOperatorRuntime{OperatorID: data.OperatorID, OperatorSessionID: data.OperatorSessionID},
		DataWorkingDirectory: data.WorkingDirectory,
	}, nil
}

// WithAnchors returns the topology with each role's enrollment-bound actuator
// key pinned from its OperatorDocument. A role whose session is missing from
// operators, or whose document binds no valid key, fails closed.
func (t CampaignOperatorTopology) WithAnchors(operators []*operatorv1.OperatorDocument) (CampaignOperatorTopology, error) {
	bind := func(role string, runtime *CampaignOperatorRuntime) error {
		for _, op := range operators {
			if op.GetId() != runtime.OperatorID || op.GetOperatorSessionId() != runtime.OperatorSessionID {
				continue
			}
			anchor, err := oracle.BoundAnchor(op)
			if err != nil {
				return fmt.Errorf("evaluation: %s operator anchor: %w", role, err)
			}
			runtime.Anchor = anchor
			return nil
		}
		return fmt.Errorf("evaluation: %s operator anchor: session %s: %w", role, runtime.OperatorSessionID, constants.ErrConformanceAnchorMissing)
	}
	if err := bind("inference", &t.Inference); err != nil {
		return CampaignOperatorTopology{}, err
	}
	if err := bind("data", &t.Data); err != nil {
		return CampaignOperatorTopology{}, err
	}
	return t, nil
}

// SharedRuntime reports whether the Inference and Data roles execute on one
// Operator runtime, and so append to one commitment ledger.
func (t CampaignOperatorTopology) SharedRuntime() bool {
	return t.Inference.OperatorID == t.Data.OperatorID
}

// Validate requires both roles to name a runtime and session.
func (t CampaignOperatorTopology) Validate() error {
	if t.Inference.OperatorID == "" || t.Inference.OperatorSessionID == "" || t.Data.OperatorID == "" || t.Data.OperatorSessionID == "" {
		return fmt.Errorf("evaluation: operator topology: %w", constants.ErrMissingRequiredField)
	}
	return nil
}

// ValidateAnchored requires both roles to name a runtime and session and to
// carry their bound anchors, as evidence verification needs. A shared runtime
// must pin one key for both roles.
func (t CampaignOperatorTopology) ValidateAnchored() error {
	if err := t.Validate(); err != nil {
		return err
	}
	for _, role := range []struct {
		name    string
		runtime CampaignOperatorRuntime
	}{{"inference", t.Inference}, {"data", t.Data}} {
		if role.runtime.Anchor == nil || role.runtime.Anchor.GetRuntimeId() != role.runtime.OperatorID {
			return fmt.Errorf("evaluation: operator topology: %s anchor is not bound to runtime %s: %w", role.name, role.runtime.OperatorID, constants.ErrConformanceAnchorMissing)
		}
	}
	if t.SharedRuntime() && !proto.Equal(t.Inference.Anchor, t.Data.Anchor) {
		return fmt.Errorf("evaluation: operator topology: shared runtime %s pins two keys: %w", t.Data.OperatorID, constants.ErrConformanceAnchorInvalid)
	}
	return nil
}

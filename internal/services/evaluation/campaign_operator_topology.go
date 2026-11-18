// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"fmt"
	"slices"
	"strings"

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

// CampaignDataOperator is one member of a run's Data fleet: the runtime the
// harness dispatches tool calls to, and the host identity the model sees in its
// roster. The identity is frozen when the topology resolves, so the roster the
// model reads never comes from a live registry read.
type CampaignDataOperator struct {
	CampaignOperatorRuntime
	WorkingDirectory string
	Hostname         string
	OS               string
	Architecture     string
}

// CampaignOperatorTopology is the set of Operator runtimes a campaign executes
// against: the Inference role serving model turns and the Data fleet executing
// tool calls. Fleet is the harness roster in roster order; a run with one Data
// Operator has a roster of one. One Operator may hold both capabilities (the
// Gateway's embedded Operator defaults to data,inference); the Inference role
// then shares a fleet member's runtime, so one commitment ledger records both
// roles' executions and evidence must claim both. Each member's working
// directory is the root of its scenario workspaces.
type CampaignOperatorTopology struct {
	Inference CampaignOperatorRuntime
	Fleet     []CampaignDataOperator
}

// DataFleetSelection names the Data Operators a run addresses. OperatorIDs pins
// stable Operator identities in roster order while allowing their active
// sessions to change. All selects every active Data session, ordered by
// Operator ID. An empty selection keeps the default single Data Operator.
type DataFleetSelection struct {
	OperatorIDs []string
	All         bool
}

// Empty reports whether the selection names no Operator and not the fleet.
func (s DataFleetSelection) Empty() bool {
	return len(s.OperatorIDs) == 0 && !s.All
}

// ResolveCampaignOperatorTopology resolves the Inference role and the Data
// fleet from the operator registry. inferenceSessionID pins the Inference
// session when set; otherwise SelectInferenceOperator's default applies. The
// Inference Operator must serve every class in hostedClasses
// (HostedProviderClasses). Anchors are not bound.
func ResolveCampaignOperatorTopology(operators []*operatorv1.OperatorDocument, inferenceSessionID string, fleet DataFleetSelection, hostedClasses []string) (CampaignOperatorTopology, error) {
	inference, err := SelectInferenceOperator(operators, inferenceSessionID, hostedClasses)
	if err != nil {
		return CampaignOperatorTopology{}, fmt.Errorf("evaluation: resolve operator topology: %w", err)
	}
	members, err := resolveDataFleet(operators, fleet)
	if err != nil {
		return CampaignOperatorTopology{}, fmt.Errorf("evaluation: resolve operator topology: %w", err)
	}
	topology := CampaignOperatorTopology{
		Inference: CampaignOperatorRuntime{OperatorID: inference.OperatorID, OperatorSessionID: inference.OperatorSessionID},
		Fleet:     make([]CampaignDataOperator, 0, len(members)),
	}
	for _, member := range members {
		topology.Fleet = append(topology.Fleet, CampaignDataOperator{
			CampaignOperatorRuntime: CampaignOperatorRuntime{OperatorID: member.OperatorID, OperatorSessionID: member.OperatorSessionID},
			WorkingDirectory:        member.WorkingDirectory,
			Hostname:                member.Hostname,
			OS:                      member.OS,
			Architecture:            member.Architecture,
		})
	}
	return topology, nil
}

// resolveDataFleet selects the Data sessions a fleet selection names.
func resolveDataFleet(operators []*operatorv1.OperatorDocument, fleet DataFleetSelection) ([]operatorcapability.DataOperatorStatus, error) {
	switch {
	case fleet.All && len(fleet.OperatorIDs) > 0:
		return nil, constants.ErrHarnessFleetSelectionConflict
	case fleet.All:
		members := operatorcapability.ActiveDataFleet(operators)
		if len(members) == 0 {
			return nil, constants.ErrDataOperatorNotFound
		}
		slices.SortFunc(members, func(a, b operatorcapability.DataOperatorStatus) int {
			return strings.Compare(a.OperatorID, b.OperatorID)
		})
		for i := 1; i < len(members); i++ {
			if members[i].OperatorID == members[i-1].OperatorID {
				return nil, fmt.Errorf("%w: operator %s has more than one active session", constants.ErrDataOperatorAmbiguous, members[i].OperatorID)
			}
		}
		return members, nil
	case len(fleet.OperatorIDs) == 0:
		selected, err := operatorcapability.SelectDataOperator(operators)
		if err != nil {
			return nil, err
		}
		return []operatorcapability.DataOperatorStatus{*selected}, nil
	}
	active := operatorcapability.ActiveDataFleet(operators)
	members := make([]operatorcapability.DataOperatorStatus, 0, len(fleet.OperatorIDs))
	seen := make(map[string]bool, len(fleet.OperatorIDs))
	for _, id := range fleet.OperatorIDs {
		if id == "" {
			return nil, fmt.Errorf("data operator id: %w", constants.ErrMissingRequiredField)
		}
		if seen[id] {
			return nil, fmt.Errorf("%w: %s", constants.ErrHarnessRosterDuplicate, id)
		}
		seen[id] = true
		var matches []operatorcapability.DataOperatorStatus
		for _, member := range active {
			if member.OperatorID == id {
				matches = append(matches, member)
			}
		}
		switch len(matches) {
		case 0:
			return nil, fmt.Errorf("%w: %s", constants.ErrDataOperatorNotFound, id)
		case 1:
			members = append(members, matches[0])
		default:
			return nil, fmt.Errorf("%w: operator %s has %d active sessions", constants.ErrDataOperatorAmbiguous, id, len(matches))
		}
	}
	return members, nil
}

// WithAnchors returns the topology with each runtime's enrollment-bound
// actuator key pinned from its OperatorDocument. A runtime whose session is
// missing from operators, or whose document binds no valid key, fails closed.
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
	fleet := slices.Clone(t.Fleet)
	for i := range fleet {
		if err := bind(fmt.Sprintf("data roster [%d]", i+1), &fleet[i].CampaignOperatorRuntime); err != nil {
			return CampaignOperatorTopology{}, err
		}
	}
	t.Fleet = fleet
	return t, nil
}

// SharedMember returns the roster index (0-based) of the fleet member whose
// runtime also serves the Inference role, and whether one does.
func (t CampaignOperatorTopology) SharedMember() (int, bool) {
	for i, member := range t.Fleet {
		if member.OperatorID == t.Inference.OperatorID {
			return i, true
		}
	}
	return -1, false
}

// Validate requires the Inference role and every fleet member to name a
// runtime and session, and the fleet to hold each Operator once.
func (t CampaignOperatorTopology) Validate() error {
	if t.Inference.OperatorID == "" || t.Inference.OperatorSessionID == "" {
		return fmt.Errorf("evaluation: operator topology: inference: %w", constants.ErrMissingRequiredField)
	}
	if len(t.Fleet) == 0 {
		return fmt.Errorf("evaluation: operator topology: %w", constants.ErrHarnessRosterEmpty)
	}
	seen := make(map[string]bool, len(t.Fleet))
	for i, member := range t.Fleet {
		if member.OperatorID == "" || member.OperatorSessionID == "" {
			return fmt.Errorf("evaluation: operator topology: data roster [%d]: %w", i+1, constants.ErrMissingRequiredField)
		}
		if seen[member.OperatorID] {
			return fmt.Errorf("evaluation: operator topology: %w: %s", constants.ErrHarnessRosterDuplicate, member.OperatorID)
		}
		seen[member.OperatorID] = true
	}
	return nil
}

// ValidateAnchored requires every runtime to name a runtime and session and to
// carry its bound anchor, as evidence verification needs. A runtime shared by
// the Inference role and a fleet member must pin one key for both.
func (t CampaignOperatorTopology) ValidateAnchored() error {
	if err := t.Validate(); err != nil {
		return err
	}
	check := func(role string, runtime CampaignOperatorRuntime) error {
		if runtime.Anchor == nil || runtime.Anchor.GetRuntimeId() != runtime.OperatorID {
			return fmt.Errorf("evaluation: operator topology: %s anchor is not bound to runtime %s: %w", role, runtime.OperatorID, constants.ErrConformanceAnchorMissing)
		}
		return nil
	}
	if err := check("inference", t.Inference); err != nil {
		return err
	}
	for i, member := range t.Fleet {
		if err := check(fmt.Sprintf("data roster [%d]", i+1), member.CampaignOperatorRuntime); err != nil {
			return err
		}
	}
	if shared, ok := t.SharedMember(); ok && !proto.Equal(t.Inference.Anchor, t.Fleet[shared].Anchor) {
		return fmt.Errorf("evaluation: operator topology: shared runtime %s pins two keys: %w", t.Inference.OperatorID, constants.ErrConformanceAnchorInvalid)
	}
	return nil
}

// RosterBinding is the fleet as the run binding freezes it, in roster order.
func (t CampaignOperatorTopology) RosterBinding() []*evalv1.EvaluationRosterMember {
	roster := make([]*evalv1.EvaluationRosterMember, 0, len(t.Fleet))
	for i, member := range t.Fleet {
		roster = append(roster, &evalv1.EvaluationRosterMember{Index: uint32(i + 1), OperatorId: member.OperatorID, OperatorSessionId: member.OperatorSessionID})
	}
	return roster
}

// FrozenRosterSelection is the fleet selection a started run re-resolves on
// execute and resume: its frozen members by Operator identity, in roster
// order. A run that froze no roster cannot execute; start a new run.
func FrozenRosterSelection(binding *evalv1.ModelCampaignBinding) (DataFleetSelection, error) {
	roster := binding.GetDataRoster()
	if len(roster) == 0 {
		return DataFleetSelection{}, fmt.Errorf("evaluation: run binding: %w", constants.ErrHarnessRosterEmpty)
	}
	selection := DataFleetSelection{OperatorIDs: make([]string, 0, len(roster))}
	for i, member := range roster {
		if member.GetIndex() != uint32(i+1) || member.GetOperatorId() == "" {
			return DataFleetSelection{}, fmt.Errorf("evaluation: run binding roster member %d: %w", i+1, constants.ErrHarnessRosterMismatch)
		}
		selection.OperatorIDs = append(selection.OperatorIDs, member.GetOperatorId())
	}
	return selection, nil
}

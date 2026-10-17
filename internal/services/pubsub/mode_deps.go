// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package pubsub

import (
	"fmt"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
)

// GovernanceCoreDeps holds the governance dependencies required by both
// outbound and gateway modes. Embedded by GatewayModeDeps and OutboundModeDeps
// so the shared fields are declared once.
type GovernanceCoreDeps struct {
	ExecutionTarget   governance.ExecutionTarget
	ReplayStore       governance.ReplayStore
	StateRootProvider governance.StateRootProvider
	TransactionAudit  governance.TransactionAuditStore
	L3Notary          governance.L3Notary
	SignerStore       governance.SignerStore
	Doctrine          *governance.L1Doctrine
}

// GatewayModeDeps embeds GovernanceCoreDeps and adds gateway-only fields.
// All fields are non-nil at construction except Consensus when L2 is not
// required; the constructor rejects nils with typed errors.
// PlatformEnrollmentDeps and GovernedDocStore are gateway-only; the compiler
// proves they do not exist in outbound mode.
type GatewayModeDeps struct {
	GovernanceCoreDeps
	GovernedDocStore       governance.GovernedDocumentStore
	ConsensusPolicyStore   governance.L2ConsensusPolicyStore
	FieldReader            mcp.FieldReader
	PlatformEnrollmentDeps *PlatformEnrollmentDeps
	Posture                constants.Posture
}

// OutboundModeDeps embeds GovernanceCoreDeps and adds the consensus policy
// the Operator verifies L2 votes against (the in-memory trust from its
// enrollment bundle). There is no GovernedDocStore, no FieldReader, no
// Consensus, no MCPGateway, no PlatformEnrollmentDeps — the type statically
// proves they do not exist in outbound mode.
type OutboundModeDeps struct {
	GovernanceCoreDeps
	ConsensusPolicyStore governance.L2ConsensusPolicyStore
}

// validateGovernanceCoreDeps checks that all fields in GovernanceCoreDeps are non-nil.
func validateGovernanceCoreDeps(core GovernanceCoreDeps) error {
	if core.ExecutionTarget == nil {
		return fmt.Errorf("mode_deps: execution target: %w", constants.ErrExecutionTargetRequired)
	}
	if core.ReplayStore == nil {
		return fmt.Errorf("mode_deps: replay store: %w", constants.ErrReplayStoreRequired)
	}
	if core.StateRootProvider == nil {
		return fmt.Errorf("mode_deps: state root provider: %w", constants.ErrStateRootProviderRequired)
	}
	if core.TransactionAudit == nil {
		return fmt.Errorf("mode_deps: transaction audit: %w", constants.ErrTransactionAuditRequired)
	}
	if core.L3Notary == nil {
		return fmt.Errorf("mode_deps: l3 notary: %w", constants.ErrL3NotaryRequired)
	}
	if core.SignerStore == nil {
		return fmt.Errorf("mode_deps: signer store: %w", constants.ErrSignerStoreRequired)
	}
	if core.Doctrine == nil {
		return fmt.Errorf("mode_deps: doctrine: %w", constants.ErrDoctrineRequired)
	}
	return nil
}

// NewOutboundModeDeps validates and constructs OutboundModeDeps.
func NewOutboundModeDeps(deps OutboundModeDeps) (*OutboundModeDeps, error) {
	if err := validateGovernanceCoreDeps(deps.GovernanceCoreDeps); err != nil {
		return nil, err
	}
	if deps.ConsensusPolicyStore == nil {
		return nil, fmt.Errorf("mode_deps: consensus policy store: %w", constants.ErrConsensusPolicyStoreRequired)
	}
	return &deps, nil
}

// NewGatewayModeDeps validates and constructs GatewayModeDeps.
func NewGatewayModeDeps(deps GatewayModeDeps) (*GatewayModeDeps, error) {
	if err := validateGovernanceCoreDeps(deps.GovernanceCoreDeps); err != nil {
		return nil, err
	}
	if deps.GovernedDocStore == nil {
		return nil, fmt.Errorf("mode_deps: governed doc store: %w", constants.ErrGovernedDocStoreRequired)
	}
	if deps.ConsensusPolicyStore == nil {
		return nil, fmt.Errorf("mode_deps: consensus policy store: %w", constants.ErrConsensusPolicyStoreRequired)
	}
	if deps.FieldReader == nil {
		return nil, fmt.Errorf("mode_deps: field reader: %w", constants.ErrFieldReaderRequired)
	}
	if deps.PlatformEnrollmentDeps == nil {
		return nil, fmt.Errorf("mode_deps: platform enrollment deps: %w", constants.ErrPlatformEnrollmentDepsRequired)
	}
	if !deps.Posture.Valid() {
		return nil, fmt.Errorf("mode_deps: posture: %w: %q", constants.ErrInvalidPosture, deps.Posture)
	}
	return &deps, nil
}

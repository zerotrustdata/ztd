// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package eval

import (
	"github.com/spf13/cobra"

	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

type governedModelMaintenanceEnv struct {
	Maintenance     evaluation.OllamaModelMaintenanceContext
	ModelDispatcher evaluation.OllamaModelCommandDispatcher

	// ResolveProbeRunner resolves the delegated inference app credential and
	// builds a capability-probe runner on demand. Only capability probing
	// dispatches governed inference (a distinct, app-credentialed Gateway
	// surface per docs/guides/connect_apps_to_gateway.md); plain registry
	// maintenance (freeze/pull/diff) never touches it, so callers must not
	// force app-credential resolution unless they actually run probes.
	ResolveProbeRunner func() (evaluation.GovernedCapabilityProbeRunner, error)

	operators []*operatorv1.OperatorDocument
}

// requireOllamaPullEnvironment sets the Ollama endpoint the `ztd operator
// model` pull and copy commands read. Inventory, residency, and release are
// typed commands the Operator serves from its own provider and need none; only
// pulls require the selected Inference Operator to serve Ollama.
func (e *governedModelMaintenanceEnv) requireOllamaPullEnvironment() error {
	endpoint, err := evaluation.GovernedInferenceOllamaEndpoint(e.operators, e.Maintenance.TargetOperatorSessionID)
	if err != nil {
		return err
	}
	e.Maintenance.Environment = modelCommandEnvironment(endpoint)
	return nil
}

// resolveGovernedModelMaintenance targets provider maintenance at the inference
// operator.
func resolveGovernedModelMaintenance(cmd *cobra.Command, deps nativeEvalDeps) (governedModelMaintenanceEnv, error) {
	gatewayDeps := deps.gatewayDeps()
	cfg, fileSvc, authContext, err := gatewayEvalEnvironment(cmd, gatewayDeps)
	if err != nil {
		return governedModelMaintenanceEnv{}, err
	}
	operators, err := evalListOperators(cmd, gatewayDeps, cfg, authContext)
	if err != nil {
		return governedModelMaintenanceEnv{}, err
	}
	inference, err := evaluation.SelectInferenceOperator(operators, "")
	if err != nil {
		return governedModelMaintenanceEnv{}, err
	}
	inferenceSessionID := inference.OperatorSessionID
	modelDispatcher, err := newHarnessOllamaModelCommandDispatcher(cfg, authContext, gatewayDeps)
	if err != nil {
		return governedModelMaintenanceEnv{}, err
	}
	prefixedNewID := adaptNewID(deps.newID)
	return governedModelMaintenanceEnv{
		Maintenance: evaluation.OllamaModelMaintenanceContext{
			TargetOperatorSessionID: inferenceSessionID,
			CaseID:                  "eval-models-maintenance",
			NewID:                   prefixedNewID,
		},
		ModelDispatcher: modelDispatcher,
		operators:       operators,
		ResolveProbeRunner: func() (evaluation.GovernedCapabilityProbeRunner, error) {
			appClient, err := inferenceEvalAppClient(cmd.Context(), cmd.ErrOrStderr(), deps, fileSvc, cfg, authContext)
			if err != nil {
				return nil, err
			}
			return evaluation.NewGovernedCapabilityProbeRunner(&governedInferenceDispatcher{client: appClient}, inferenceSessionID, prefixedNewID), nil
		},
	}, nil
}

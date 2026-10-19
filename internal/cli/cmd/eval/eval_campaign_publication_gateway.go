// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package eval

import (
	"github.com/spf13/cobra"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/gwremote"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

// NewCampaignPublicationCoordinator wires the host-side publication of run
// lifecycle projections to the Gateway mirror. cfg is the command's loaded
// config, so the Gateway client unseals the CLI identity under its master key.
func NewCampaignPublicationCoordinator(cmd *cobra.Command, fileSvc fs.RuntimeFileService, cfg *config.Config) (*evaluation.CampaignPublicationCoordinator, error) {
	exporter, err := gwremote.NewCampaignFeedExporter(shared.CommandContext(cmd), fileSvc, cfg)
	if err != nil {
		return nil, err
	}
	proofPublisher, err := gwremote.NewCampaignProofPublisher(shared.CommandContext(cmd), fileSvc, cfg)
	if err != nil {
		return nil, err
	}
	publicationState, err := gwremote.NewCampaignPublicationStateStore(fileSvc, cfg)
	if err != nil {
		return nil, err
	}
	control, err := newOSProcessControl(fileSvc)
	if err != nil {
		return nil, err
	}
	coordinator := evaluation.NewCampaignPublicationCoordinator(
		evaluation.NewStore(fileSvc),
		fileSvc,
		publicationState,
		exporter,
	).WithProofPublisher(proofPublisher).WithLeaseLiveness(leaseLiveness(control))
	if gwremote.IsGatewayHealthy() {
		coordinator.WithMirrorProbe(gwremote.NewHTTPCampaignMirrorProbe(shared.CommandContext(cmd)))
	}
	return coordinator, nil
}

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package authcmd

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/cli/output"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

func consensusEnrollmentCmd() *cobra.Command {
	return consensusEnrollmentCmdWithConfig(shared.LoadConfig, DefaultAPIClientFactory, shared.NewFileSvc)
}

func consensusEnrollmentCmdWithConfig(
	configLoader func(string) (*config.Config, error),
	clientFactory APIClientFactory,
	fileSvcFactory func(string, *slog.Logger) (fs.RuntimeFileService, error),
) *cobra.Command {
	newClient := func() (APIClient, error) {
		cfg, err := configLoader("")
		if err != nil {
			return nil, err
		}
		fileSvc, err := fileSvcFactory("", slog.Default())
		if err != nil {
			return nil, fmt.Errorf("%w: %w", constants.ErrFileServiceInit, err)
		}
		client, err := clientFactory(fileSvc, cfg)
		if err != nil {
			return nil, fmt.Errorf("consensus enrollment client: %w", err)
		}
		return client, nil
	}

	cmd := &cobra.Command{
		Use:   "consensus",
		Short: "Bind or inspect the enrolled consensus application",
		Long: `Manage the Gateway's single consensus application using the authenticated owner's CLI identity.
Enroll the application with auth enroll app <name> --signing-key before binding it.
Binding traverses governance and requires passkey approval under ratify and notary.
A pending binding prints its approval URL and transaction; use show after approval to inspect the binding.`,
	}
	cmd.AddCommand(consensusBindCmd(newClient), consensusShowCmd(newClient))
	return cmd
}

func consensusBindCmd(newClient func() (APIClient, error)) *cobra.Command {
	return &cobra.Command{
		Use:   "bind <app-name> <https-endpoint>",
		Short: "Bind an enrolled application through governance",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			request := models.PlatformEnrollmentBindConsensusRequest{
				AppID:       strings.TrimSpace(args[0]),
				EndpointURL: strings.TrimSpace(args[1]),
			}
			if request.AppID == "" {
				return fmt.Errorf("consensus binding app name: %w", constants.ErrMissingRequiredField)
			}
			if err := models.ValidateConsensusEndpoint(request.EndpointURL); err != nil {
				return err
			}
			client, err := newClient()
			if err != nil {
				return err
			}
			body, err := client.Post(constants.APIPaths.AuthPlatformEnrollmentConsensus, request)
			if err != nil {
				return fmt.Errorf("consensus bind: %w", err)
			}
			var suspension models.PlatformEnrollmentL3SuspensionResponse
			if json.Unmarshal(body, &suspension) == nil && suspension.Status == models.PlatformEnrollmentStatusL3Required {
				if output.JSONEnabled(cmd) {
					return output.WriteJSON(cmd.OutOrStdout(), suspension)
				}
				return auth.WritePlatformEnrollmentSuspension(cmd.OutOrStdout(), suspension)
			}
			policy, err := decodeConsensusBinding(body)
			if err != nil {
				return err
			}
			if policy.MemberAppIDs[0] != request.AppID || policy.EndpointURL != request.EndpointURL {
				return fmt.Errorf("consensus bind: binding does not match request: %w", constants.ErrInvalidJSONResponse)
			}
			return output.WriteJSON(cmd.OutOrStdout(), policy)
		},
	}
}

func consensusShowCmd(newClient func() (APIClient, error)) *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Print the current consensus binding as JSON",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := newClient()
			if err != nil {
				return err
			}
			body, err := client.Get(constants.APIPaths.AuthPlatformEnrollmentConsensus)
			if err != nil {
				return fmt.Errorf("consensus show: %w", err)
			}
			policy, err := decodeConsensusBinding(body)
			if err != nil {
				return err
			}
			return output.WriteJSON(cmd.OutOrStdout(), policy)
		},
	}
}

// decodeConsensusBinding accepts only the single-application binding shape the
// Gateway's governed binding handler writes.
func decodeConsensusBinding(body []byte) (*models.ConsensusPolicy, error) {
	var policy models.ConsensusPolicy
	if err := json.Unmarshal(body, &policy); err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrInvalidJSONResponse, err)
	}
	if policy.ID != string(constants.DocIDConsensusBinding) || !policy.Enabled || len(policy.MemberAppIDs) != 1 || policy.Quorum != 1 {
		return nil, fmt.Errorf("consensus binding shape: %w", constants.ErrInvalidJSONResponse)
	}
	if err := models.ValidateConsensusEndpoint(policy.EndpointURL); err != nil {
		return nil, fmt.Errorf("consensus binding endpoint: %w: %w", constants.ErrInvalidJSONResponse, err)
	}
	return &policy, nil
}

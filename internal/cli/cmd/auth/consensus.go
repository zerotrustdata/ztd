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
		Long: `Manage the Gateway's K-of-N consensus application set using the authenticated owner's CLI identity.
Enroll each member with auth enroll app <name> --signing-key before binding it; use --member-app for every member, including the primary app.
Binding traverses governance and requires passkey approval under ratify and notary.
A pending binding prints its approval URL and transaction; use show after approval to inspect the binding.`,
	}
	cmd.AddCommand(consensusBindCmd(newClient), consensusShowCmd(newClient))
	return cmd
}

func consensusBindCmd(newClient func() (APIClient, error)) *cobra.Command {
	var memberAppIDs []string
	var quorum uint32
	cmd := &cobra.Command{
		Use:   "bind <app-name> <https-endpoint>",
		Short: "Bind an enrolled application through governance",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("quorum") && quorum == 0 {
				return constants.ErrConsensusQuorumInvalid
			}
			// Normalize member IDs exactly as the Gateway does so the
			// post-bind comparison matches the stored policy.
			var normalizedMemberAppIDs []string
			for _, id := range memberAppIDs {
				id = strings.TrimSpace(id)
				if id == "" {
					return fmt.Errorf("consensus binding member app: %w", constants.ErrMissingRequiredField)
				}
				normalizedMemberAppIDs = append(normalizedMemberAppIDs, id)
			}
			request := models.PlatformEnrollmentBindConsensusRequest{
				AppID:        strings.TrimSpace(args[0]),
				EndpointURL:  strings.TrimSpace(args[1]),
				MemberAppIDs: normalizedMemberAppIDs,
				Quorum:       int(quorum),
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
			if len(request.MemberAppIDs) == 0 {
				request.MemberAppIDs = []string{request.AppID}
			}
			if request.Quorum == 0 {
				request.Quorum = 1
			}
			if (policy.AppID != "" && policy.AppID != request.AppID) || !sameStringList(policy.MemberAppIDs, request.MemberAppIDs) || policy.Quorum != request.Quorum || policy.EndpointURL != request.EndpointURL {
				return fmt.Errorf("consensus bind: binding does not match request: %w", constants.ErrInvalidJSONResponse)
			}
			return output.WriteJSON(cmd.OutOrStdout(), policy)
		},
	}
	cmd.Flags().StringArrayVar(&memberAppIDs, "member-app", nil, "Consensus member application ID (repeat for each member; primary app must be included)")
	cmd.Flags().Uint32Var(&quorum, "quorum", 0, "Required affirmative signature count; required when more than one --member-app is given, defaults to 1 for a single application")
	return cmd
}

func sameStringList(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
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

// decodeConsensusBinding accepts only a valid binding shape written by the
// Gateway's governed binding handler.
func decodeConsensusBinding(body []byte) (*models.ConsensusPolicy, error) {
	var policy models.ConsensusPolicy
	if err := json.Unmarshal(body, &policy); err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrInvalidJSONResponse, err)
	}
	if policy.ID != string(constants.DocIDConsensusBinding) || !policy.Enabled || len(policy.MemberAppIDs) == 0 || policy.Quorum < 1 || policy.Quorum > len(policy.MemberAppIDs) {
		return nil, fmt.Errorf("consensus binding shape: %w", constants.ErrInvalidJSONResponse)
	}
	seen := make(map[string]struct{}, len(policy.MemberAppIDs))
	for _, id := range policy.MemberAppIDs {
		if strings.TrimSpace(id) == "" {
			return nil, fmt.Errorf("consensus binding shape: %w", constants.ErrInvalidJSONResponse)
		}
		if _, ok := seen[id]; ok {
			return nil, fmt.Errorf("consensus binding shape: %w", constants.ErrInvalidJSONResponse)
		}
		seen[id] = struct{}{}
	}
	if err := models.ValidateConsensusEndpoint(policy.EndpointURL); err != nil {
		return nil, fmt.Errorf("consensus binding endpoint: %w: %w", constants.ErrInvalidJSONResponse, err)
	}
	return &policy, nil
}

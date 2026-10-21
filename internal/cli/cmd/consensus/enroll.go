// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package consensuscmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zerotrustdata/ztd/internal/cli/auth"
	authcmd "github.com/zerotrustdata/ztd/internal/cli/cmd/auth"
	"github.com/zerotrustdata/ztd/internal/cli/output"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

func enrollCmd(newClient func() (authcmd.APIClient, error)) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "enroll",
		Short: "Bind an enrolled application as the L2 consensus",
		Long: `Bind an enrolled application as the Gateway's L2 consensus through governance.

  app       Bind an application whose HTTPS deliberation endpoint returns signed votes.
  tribunal  Bind the Gateway-hosted Tribunal panel pinned to one frozen model tag and digest.

Bare ` + "`ztd consensus enroll`" + ` (no subcommand) prints this help and exits non-zero.`,
	}
	cmd.AddCommand(enrollAppCmd(newClient), enrollTribunalCmd(newClient, defaultTribunalEnrollDeps()))
	return cmd
}

func enrollAppCmd(newClient func() (authcmd.APIClient, error)) *cobra.Command {
	var memberAppIDs []string
	var quorum uint32
	cmd := &cobra.Command{
		Use:   "app <primary-app> <https-endpoint>",
		Short: "Bind an enrolled application through governance",
		Long: `Bind an enrolled application as the L2 consensus through governance.

Enroll each member with ztd auth enroll app <name> --signing-key before binding it; use --member-app for every member, including the primary app.
Binding traverses governance and requires passkey approval under ratify and notary.
A pending binding prints its approval URL and transaction; use ztd consensus status after approval to inspect the binding.`,
		Args: cobra.ExactArgs(2),
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
					return fmt.Errorf("consensus enroll app member: %w", constants.ErrMissingRequiredField)
				}
				normalizedMemberAppIDs = append(normalizedMemberAppIDs, id)
			}
			request := models.PlatformEnrollmentBindConsensusRequest{
				AppID:        strings.TrimSpace(args[0]),
				EndpointURL:  strings.TrimSpace(args[1]),
				MemberAppIDs: normalizedMemberAppIDs,
				Quorum:       int(quorum),
				Provider:     string(constants.ConsensusProviderApplication),
			}
			if request.AppID == "" {
				return fmt.Errorf("consensus enroll app name: %w", constants.ErrMissingRequiredField)
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
				return fmt.Errorf("consensus enroll app: %w", err)
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
			members, wantQuorum, err := models.ResolveConsensusMembership(request.AppID, request.MemberAppIDs, request.Quorum)
			if err != nil {
				return fmt.Errorf("consensus enroll app: %w", err)
			}
			if (policy.AppID != "" && policy.AppID != request.AppID) || !sameStringList(policy.MemberAppIDs, members) || policy.Quorum != wantQuorum || policy.EndpointURL != request.EndpointURL {
				return fmt.Errorf("consensus enroll app: binding does not match request: %w", constants.ErrInvalidJSONResponse)
			}
			return output.WriteJSON(cmd.OutOrStdout(), policy)
		},
	}
	cmd.Flags().StringArrayVar(&memberAppIDs, "member-app", nil, "Consensus member application ID (repeat for each member; primary app must be included)")
	cmd.Flags().Uint32Var(&quorum, "quorum", 0, "Required affirmative signature count; required when more than one --member-app is given, defaults to 1 for a single application")
	return cmd
}

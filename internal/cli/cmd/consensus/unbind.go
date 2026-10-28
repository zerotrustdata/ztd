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

	"github.com/spf13/cobra"

	"github.com/zerotrustdata/ztd/internal/cli/auth"
	authcmd "github.com/zerotrustdata/ztd/internal/cli/cmd/auth"
	"github.com/zerotrustdata/ztd/internal/cli/output"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

func unbindCmd(newClient func() (authcmd.APIClient, error)) *cobra.Command {
	return &cobra.Command{
		Use:   "unbind",
		Short: "Clear the bound L2 consensus through governance",
		Long: `Clear the Gateway's bound L2 consensus application through governance.

Unbinding is owner-only and goes through the governed PLATFORM_ENROLLMENT_UNBIND_CONSENSUS action.
Under ratify and notary it requires passkey approval: a pending unbind prints its approval URL and transaction.
Use ztd consensus status after approval to confirm the Gateway reports no binding.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := newClient()
			if err != nil {
				return err
			}
			body, err := client.Delete(constants.APIPaths.AuthPlatformEnrollmentConsensus)
			if err != nil {
				return fmt.Errorf("consensus unbind: %w", err)
			}
			var suspension models.PlatformEnrollmentL3SuspensionResponse
			if json.Unmarshal(body, &suspension) == nil && suspension.Status == models.PlatformEnrollmentStatusL3Required {
				if output.JSONEnabled(cmd) {
					return output.WriteJSON(cmd.OutOrStdout(), suspension)
				}
				return auth.WritePlatformEnrollmentSuspension(cmd.OutOrStdout(), suspension)
			}
			var policy models.ConsensusPolicy
			if err := json.Unmarshal(body, &policy); err != nil {
				return fmt.Errorf("consensus unbind: %w: %w", constants.ErrInvalidJSONResponse, err)
			}
			// The Gateway returns the binding with enabled=false once cleared.
			if policy.ID != string(constants.DocIDConsensusBinding) || policy.Enabled {
				return fmt.Errorf("consensus unbind: binding shape: %w", constants.ErrInvalidJSONResponse)
			}
			return output.WriteJSON(cmd.OutOrStdout(), policy)
		},
	}
}

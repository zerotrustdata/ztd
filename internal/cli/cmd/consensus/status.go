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
	"io"
	"strings"

	"github.com/spf13/cobra"

	authcmd "github.com/zerotrustdata/ztd/internal/cli/cmd/auth"
	"github.com/zerotrustdata/ztd/internal/cli/output"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

// consensusStatus is the typed output of `ztd consensus status`.
type consensusStatus struct {
	Bound           bool              `json:"bound"`
	Provider        string            `json:"provider,omitempty"`
	Quorum          int               `json:"quorum,omitempty"`
	ModelTag        string            `json:"model_tag,omitempty"`
	Members         []string          `json:"members"`
	Usable          int               `json:"usable"`
	Ready           bool              `json:"ready"`
	Posture         constants.Posture `json:"posture"`
	GovernanceReady bool              `json:"governance_ready"`
	L2Enforced      bool              `json:"l2_enforced"`
}

func statusCmd(newClient func() (authcmd.APIClient, error)) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the bound consensus, signer readiness, and posture",
		Long: `Show the Gateway's bound L2 consensus: provider, quorum K-of-N, pinned model tag (tribunal),
member application IDs, usable signers, governance posture, and whether L2 is enforced.

Exits non-zero when the posture requires L2 consensus and no application is bound.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := newClient()
			if err != nil {
				return err
			}
			binding, err := fetchBinding(client)
			if err != nil {
				return fmt.Errorf("consensus status: %w", err)
			}
			healthBody, err := client.Get(constants.APIPaths.Health)
			if err != nil {
				return fmt.Errorf("consensus status: health: %w", err)
			}
			var health models.HealthResponse
			if err := json.Unmarshal(healthBody, &health); err != nil {
				return fmt.Errorf("consensus status: health: %w: %w", constants.ErrInvalidJSONResponse, err)
			}
			status, err := buildStatus(binding, health)
			if err != nil {
				return err
			}
			if output.JSONEnabled(cmd) {
				if err := output.WriteJSON(cmd.OutOrStdout(), status); err != nil {
					return err
				}
			} else {
				writeStatusText(cmd.OutOrStdout(), status)
			}
			if status.L2Enforced && !status.Bound {
				return fmt.Errorf("consensus status: posture %s requires L2 consensus: %w", status.Posture, constants.ErrTxL2ConsensusNotConfigured)
			}
			return nil
		},
	}
}

// buildStatus joins the enabled binding (nil when unbound) with the /health
// readiness summary. Usable and Ready come from /health, the single readiness view.
func buildStatus(binding *models.ConsensusPolicy, health models.HealthResponse) (consensusStatus, error) {
	status := consensusStatus{
		Members:         []string{},
		Usable:          health.Consensus.Usable,
		Ready:           health.Consensus.Ready,
		Posture:         health.Posture,
		GovernanceReady: health.GovernanceReady,
		L2Enforced:      health.Posture.RequiresL2(),
	}
	if binding == nil {
		return status, nil
	}
	provider, err := binding.ConsensusProvider()
	if err != nil {
		return consensusStatus{}, fmt.Errorf("consensus status: %w", err)
	}
	status.Bound = true
	status.Provider = string(provider)
	status.Quorum = binding.Quorum
	status.ModelTag = binding.ModelTag
	status.Members = binding.MemberAppIDs
	return status, nil
}

func writeStatusText(w io.Writer, s consensusStatus) {
	if !s.Bound {
		fmt.Fprintln(w, "Consensus  not bound")
	} else {
		fmt.Fprintln(w, "Consensus  bound")
		fmt.Fprintf(w, "Provider   %s\n", s.Provider)
		fmt.Fprintf(w, "Quorum     %d-of-%d\n", s.Quorum, len(s.Members))
		if s.ModelTag != "" {
			fmt.Fprintf(w, "Model      %s\n", s.ModelTag)
		}
		fmt.Fprintf(w, "Members    %s\n", strings.Join(s.Members, ", "))
		readiness := "not ready"
		if s.Ready {
			readiness = "ready"
		}
		fmt.Fprintf(w, "Signers    %d/%d usable (%s)\n", s.Usable, len(s.Members), readiness)
	}
	l2 := "not enforced"
	if s.L2Enforced {
		l2 = "enforced"
	}
	fmt.Fprintf(w, "Posture    %s\n", s.Posture)
	fmt.Fprintf(w, "L2         %s\n", l2)
}

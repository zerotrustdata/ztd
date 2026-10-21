// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package consensuscmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zerotrustdata/ztd/internal/cli/api"
	authcmd "github.com/zerotrustdata/ztd/internal/cli/cmd/auth"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

// Cmd is the `ztd consensus` group: inspect and bind the Gateway's L2 consensus.
func Cmd() *cobra.Command {
	return cmdWithConfig(shared.LoadConfig, authcmd.DefaultAPIClientFactory, shared.NewFileSvc)
}

func cmdWithConfig(
	configLoader func(string) (*config.Config, error),
	clientFactory authcmd.APIClientFactory,
	fileSvcFactory func(string, *slog.Logger) (fs.RuntimeFileService, error),
) *cobra.Command {
	newClient := func() (authcmd.APIClient, error) {
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
			return nil, fmt.Errorf("consensus client: %w", err)
		}
		return client, nil
	}

	cmd := &cobra.Command{
		Use:   "consensus",
		Short: "Inspect and bind the Gateway's L2 consensus application set",
		Long: `Inspect and bind the Gateway's K-of-N L2 consensus using the authenticated owner's CLI identity.

  status    Show the bound consensus, signer readiness, and posture.
  enroll    Bind an enrolled application or the Tribunal panel as the consensus (see ztd consensus enroll --help).

Enroll each member with ztd auth enroll app <name> --signing-key before binding it; include every member,
including the primary app, with --member-app.`,
	}
	cmd.AddCommand(statusCmd(newClient), enrollCmd(newClient))
	return cmd
}

// fetchBinding returns the enabled consensus binding, or nil when the Gateway
// reports none (HTTP 404). Any other failure is an error.
func fetchBinding(client authcmd.APIClient) (*models.ConsensusPolicy, error) {
	body, err := client.Get(constants.APIPaths.AuthPlatformEnrollmentConsensus)
	if err != nil {
		var statusErr *api.StatusError
		if errors.As(err, &statusErr) && statusErr.StatusCode == http.StatusNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("consensus binding: %w", err)
	}
	return decodeConsensusBinding(body)
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
	provider, err := policy.ConsensusProvider()
	if err != nil {
		return nil, fmt.Errorf("consensus binding provider: %w: %w", constants.ErrInvalidJSONResponse, err)
	}
	// models.ResolveConsensusBinding owns provider-specific binding shape: a
	// tribunal binding is Gateway-hosted and carries no endpoint.
	if _, err := models.ResolveConsensusBinding(models.ConsensusBindingSpec{
		Provider:     provider,
		AppID:        policy.AppID,
		EndpointURL:  policy.EndpointURL,
		MemberAppIDs: policy.MemberAppIDs,
		Quorum:       policy.Quorum,
		ModelTag:     policy.ModelTag,
		ModelDigest:  policy.ModelDigest,
	}); err != nil {
		return nil, fmt.Errorf("consensus binding %s: %w: %w", provider, constants.ErrInvalidJSONResponse, err)
	}
	return &policy, nil
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

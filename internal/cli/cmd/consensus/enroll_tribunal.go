// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package consensuscmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zerotrustdata/ztd/internal/cli/auth"
	authcmd "github.com/zerotrustdata/ztd/internal/cli/cmd/auth"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/cli/output"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/tribunal"
)

// tribunalEnrollDeps are the seams `enroll tribunal` composes. Production wires
// the real config loader, runtime file service, sealed-key check, and app
// enroller; tests substitute fakes.
type tribunalEnrollDeps struct {
	configLoader    func(string) (*config.Config, error)
	fileSvcFactory  func(string, *slog.Logger) (fs.RuntimeFileService, error)
	enrollerFactory authcmd.AppEnrollerFactory
	// memberEnrolled reports whether appID's sealed signing key already exists.
	memberEnrolled func(ctx context.Context, fileSvc fs.RuntimeFileService, cfg *config.Config, appID string) bool
}

func defaultTribunalEnrollDeps() tribunalEnrollDeps {
	return tribunalEnrollDeps{
		configLoader:    shared.LoadConfig,
		fileSvcFactory:  shared.NewFileSvc,
		enrollerFactory: authcmd.DefaultAppEnrollerFactory,
		memberEnrolled: func(ctx context.Context, fileSvc fs.RuntimeFileService, cfg *config.Config, appID string) bool {
			return auth.NewTribunalConsensusSigner(ctx, fileSvc, cfg).HasKey(appID)
		},
	}
}

func enrollTribunalCmd(newClient func() (authcmd.APIClient, error), deps tribunalEnrollDeps) *cobra.Command {
	var model string
	var quorum int
	memberAppIDs := tribunal.ConsensusMemberAppIDs()
	cmd := &cobra.Command{
		Use:   "tribunal",
		Short: "Bind the Gateway-hosted Tribunal panel as the L2 consensus pinned to a frozen model",
		Long: `Bind the Gateway-hosted Tribunal panel as the L2 consensus through governance, pinned to one frozen model.

--model names a served model tag in the frozen model registry; its digest is pinned in the binding.
Each panel member (tribunal-axiom, tribunal-concord, tribunal-variance, tribunal-pragma, tribunal-nemesis)
is enrolled with a sealed signing key unless its key already exists locally, so a re-run resumes.
Binding traverses governance and requires passkey approval under ratify and notary.
A pending binding prints its approval URL and transaction; use ztd consensus status after approval to inspect the binding.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			tag := strings.TrimSpace(model)
			if tag == "" {
				return fmt.Errorf("consensus enroll tribunal model: %w", constants.ErrMissingRequiredField)
			}
			if quorum < constants.TribunalConsensusMinQuorum || quorum > len(memberAppIDs) {
				return constants.ErrConsensusQuorumInvalid
			}
			ctx := cmd.Context()
			cfg, err := deps.configLoader("")
			if err != nil {
				return err
			}
			fileSvc, err := deps.fileSvcFactory("", slog.Default())
			if err != nil {
				return fmt.Errorf("%w: %w", constants.ErrFileServiceInit, err)
			}
			digest, err := resolveTribunalModelDigest(ctx, fileSvc, tag)
			if err != nil {
				return err
			}
			client, err := newClient()
			if err != nil {
				return err
			}
			for _, appID := range memberAppIDs {
				if deps.memberEnrolled(ctx, fileSvc, cfg, appID) {
					continue
				}
				if err := enrollTribunalMember(ctx, cmd.OutOrStdout(), client, fileSvc, cfg, deps.enrollerFactory, appID); err != nil {
					return err
				}
			}
			request := models.PlatformEnrollmentBindConsensusRequest{
				AppID:        memberAppIDs[0],
				MemberAppIDs: memberAppIDs,
				Quorum:       quorum,
				Provider:     string(constants.ConsensusProviderTribunal),
				ModelTag:     tag,
				ModelDigest:  digest,
			}
			body, err := client.Post(constants.APIPaths.AuthPlatformEnrollmentConsensus, request)
			if err != nil {
				return fmt.Errorf("consensus enroll tribunal: %w", err)
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
			if err := verifyTribunalBinding(policy, request); err != nil {
				return err
			}
			return output.WriteJSON(cmd.OutOrStdout(), policy)
		},
	}
	cmd.Flags().StringVar(&model, "model", "", "Served model tag from the frozen model registry to pin (required)")
	cmd.Flags().IntVar(&quorum, "quorum", constants.TribunalConsensusDefaultQuorum, fmt.Sprintf("Required affirmative signature count, between %d and %d", constants.TribunalConsensusMinQuorum, len(memberAppIDs)))
	return cmd
}

// resolveTribunalModelDigest returns the normalized digest of the frozen
// registry variant whose served tag is tag. An unfrozen registry or an absent
// tag wraps constants.ErrNotFound.
func resolveTribunalModelDigest(ctx context.Context, fileSvc fs.RuntimeFileService, tag string) (string, error) {
	exists, err := fileSvc.FileExists(ctx, evaluation.DefaultModelInventoryRelPath)
	if err != nil {
		return "", fmt.Errorf("consensus enroll tribunal: check model registry: %w", err)
	}
	if !exists {
		return "", fmt.Errorf("consensus enroll tribunal: model registry is not frozen, model tag %q not found: %w", tag, constants.ErrNotFound)
	}
	freeze, err := evaluation.LoadModelInventoryFreezeFromRuntime(ctx, fileSvc, evaluation.DefaultModelInventoryRelPath)
	if err != nil {
		return "", fmt.Errorf("consensus enroll tribunal: load model registry: %w", err)
	}
	for _, variant := range freeze.Variants {
		if variant.GetServedModelTag() != tag {
			continue
		}
		digest, err := models.NormalizeProviderModelDigest(variant.GetModelDigest())
		if err != nil {
			return "", fmt.Errorf("consensus enroll tribunal: model tag %q digest: %w", tag, err)
		}
		return digest, nil
	}
	return "", fmt.Errorf("consensus enroll tribunal: model tag %q not found in model registry: %w", tag, constants.ErrNotFound)
}

// enrollTribunalMember runs the owner-approved app enrollment for one panel
// member with a sealed Ed25519 signing identity, exactly as
// `ztd auth enroll app <name> --signing-key` does.
func enrollTribunalMember(ctx context.Context, out io.Writer, client authcmd.APIClient, fileSvc fs.RuntimeFileService, cfg *config.Config, factory authcmd.AppEnrollerFactory, appID string) error {
	opt := auth.AppEnrollmentOptions{
		GenerateSigningKey: true,
		Approver:           auth.OwnerAppEnrollmentApprover(client, "ztd consensus enroll tribunal: enrolled by the authenticated owner"),
	}
	enroller, err := factory(appID, fileSvc, cfg, slog.Default(), opt)
	if err != nil {
		return fmt.Errorf("consensus enroll tribunal member %s: %w", appID, err)
	}
	if _, err := enroller.Enroll(ctx, out); err != nil {
		return fmt.Errorf("consensus enroll tribunal member %s: %w", appID, err)
	}
	return nil
}

// verifyTribunalBinding confirms the Gateway stored the binding that was
// requested: provider, primary app, member set, quorum, and pinned model.
func verifyTribunalBinding(policy *models.ConsensusPolicy, request models.PlatformEnrollmentBindConsensusRequest) error {
	provider, err := policy.ConsensusProvider()
	if err != nil {
		return fmt.Errorf("consensus enroll tribunal: %w", err)
	}
	if provider != constants.ConsensusProviderTribunal ||
		policy.AppID != request.AppID ||
		!sameStringList(policy.MemberAppIDs, request.MemberAppIDs) ||
		policy.Quorum != request.Quorum ||
		policy.ModelTag != request.ModelTag ||
		policy.ModelDigest != request.ModelDigest ||
		policy.EndpointURL != "" {
		return fmt.Errorf("consensus enroll tribunal: binding does not match request: %w", constants.ErrInvalidJSONResponse)
	}
	return nil
}

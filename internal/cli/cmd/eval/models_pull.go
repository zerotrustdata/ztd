// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package eval

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/zerotrustdata/ztd/internal/cli/output"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// pullCatalogVariants presents pull catalog entries as model variants so the
// shared selector can choose among them.
func pullCatalogVariants(models []evaluation.ModelPullEntry) []*evalv1.ModelVariant {
	variants := make([]*evalv1.ModelVariant, 0, len(models))
	for _, model := range models {
		parameterCount, err := evaluation.ParseParameterCount(model.ParameterCount)
		if err != nil {
			parameterCount = 0
		}
		variants = append(variants, &evalv1.ModelVariant{
			VariantId:      model.VariantID,
			ServedModelTag: model.ServedModelTag,
			ModelFamily:    model.ModelFamily,
			ParameterCount: parameterCount,
		})
	}
	return variants
}

func modelsPullCmd(deps nativeEvalDeps) *cobra.Command {
	var timeout time.Duration
	var dryRun bool
	var formations bool
	var selector ModelSelector
	cmd := &cobra.Command{
		Use:   "pull <selector>",
		Short: "Pull models onto the inference provider through the governed Inference Operator",
		Long: `Pull models through the governed Inference Operator and apply their canonical aliases.

Pull metadata comes from eval/model-pull-catalog.json. With --formations the
sovereign ExecutionTopologies served tags are pulled instead.

Examples:
  ztd eval models pull qwen3:4b
  ztd eval models pull --family glm53 --dry-run
  ztd eval models pull --formations --timeout 24h`,
		RunE: func(cmd *cobra.Command, args []string) error {
			selector = *selector.withArgs(args)
			if formations && selector.IsSet() {
				return fmt.Errorf("evaluation: models pull: --formations cannot be combined with a model selector: %w", constants.ErrEvaluationFlagsInvalid)
			}
			cfg, _, err := nativeEvalEnvironment(cmd, deps)
			if err != nil {
				return err
			}
			catalogPath := filepath.Join(cfg.ProjectRoot, evaluation.DefaultModelPullCatalogRelPath)
			var variantIDs []string
			if !formations {
				catalog, err := evaluation.LoadModelPullCatalog(catalogPath)
				if err != nil {
					return fmt.Errorf("evaluation: models pull: %w", err)
				}
				selected, err := selector.Resolve(pullCatalogVariants(catalog.Models))
				if err != nil {
					return fmt.Errorf("evaluation: models pull: %w", err)
				}
				for _, variant := range selected {
					variantIDs = append(variantIDs, variant.GetVariantId())
				}
			}
			env, err := resolveGovernedModelMaintenance(cmd, deps)
			if err != nil {
				return fmt.Errorf("evaluation: models pull: %w", err)
			}
			if err := evaluation.RequireOllamaInferenceOperator(env.operators, env.Maintenance.TargetOperatorSessionID); err != nil {
				return fmt.Errorf("evaluation: models pull: %w", err)
			}
			req := evaluation.ModelPullRequest{
				CatalogPath:        catalogPath,
				Timeout:            timeout,
				VariantIDs:         variantIDs,
				DryRun:             dryRun,
				Dispatcher:         env.ModelDispatcher,
				InferenceSessionID: env.Maintenance.TargetOperatorSessionID,
				NewID:              env.Maintenance.NewID,
				CaseID:             "eval-models-pull",
				Progress: func(event evaluation.ModelPullEvent) {
					if output.JSONEnabled(cmd) {
						return
					}
					target := event.ServedModelTag
					if target == "" {
						target = event.VariantID
					}
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s %s: %s", target, event.Action, event.Status)
					showDetail := event.Action != evaluation.ModelPullActionPull || event.Status == evaluation.ModelPullStatusFailed
					if strings.TrimSpace(event.Detail) != "" && showDetail {
						_, _ = fmt.Fprintf(cmd.OutOrStdout(), " (%s)", event.Detail)
					}
					_, _ = fmt.Fprintln(cmd.OutOrStdout())
				},
			}
			var result *evaluation.ModelPullResult
			if formations {
				result, err = evaluation.PullFormationModels(cmd.Context(), req)
			} else {
				result, err = evaluation.PullCatalogModels(cmd.Context(), req)
			}
			if err != nil {
				return fmt.Errorf("evaluation: models pull: %w", err)
			}
			if output.JSONEnabled(cmd) {
				return output.WriteJSON(cmd.OutOrStdout(), result)
			}
			out := cmd.OutOrStdout()
			for _, tag := range result.Pulled {
				_, _ = fmt.Fprintf(out, "Pulled %s\n", tag)
			}
			for _, tag := range result.Aliased {
				_, _ = fmt.Fprintf(out, "Aliased %s\n", tag)
			}
			for _, skip := range result.Skipped {
				_, _ = fmt.Fprintf(out, "Skipped %s: %s\n", skip.ServedModelTag, skip.Reason)
			}
			for _, failure := range result.Failed {
				_, _ = fmt.Fprintf(out, "Failed %s: %s\n", failure.ServedModelTag, failure.Err)
			}
			if len(result.Failed) > 0 {
				causes := make([]error, 0, len(result.Failed))
				for _, failure := range result.Failed {
					causes = append(causes, failure.Cause)
				}
				return fmt.Errorf("evaluation: models pull: %d model(s) failed: %w", len(result.Failed), errors.Join(causes...))
			}
			_, err = fmt.Fprintln(out, "\nNext, bind the registry to the pulled digests with:\nztd eval models freeze")
			return err
		},
	}
	cmd.Flags().DurationVar(&timeout, "timeout", constants.ModelPullDefaultTimeout, "Maximum time to wait for each pull")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print the pull plan without pulling")
	cmd.Flags().BoolVar(&formations, "formations", false, "Pull the sovereign ExecutionTopologies served tags")
	selector.bindFlags(cmd)
	return cmd
}

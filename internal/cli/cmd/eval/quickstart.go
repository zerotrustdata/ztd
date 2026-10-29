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
	"io"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zerotrustdata/ztd/internal/cli/output"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

const (
	// quickstartDefaultModel is the light registry model a quickstart scores
	// when neither --model nor --formation is given.
	quickstartDefaultModel = "granite4:1b-h"
	// quickstartCampaignPrefix names every campaign the quickstart freezes.
	quickstartCampaignPrefix = "quickstart"
)

// quickstartScreenJudges are light registry models, in preference order, that
// the next-step hint proposes as the smoke-suite judge: the first one that is
// not a subject model, so no subject grades itself.
var quickstartScreenJudges = []string{"granite4:1b-h", "llama3.2:1b", "granite3.3:2b"}

type quickstartOptions struct {
	Model          string
	Formation      string
	Judge          string
	Publish        bool
	RequireWitness bool
}

type quickstartResultJSON struct {
	CampaignID string `json:"campaign_id"`
	RunID      string `json:"run_id"`
	Subject    string `json:"subject"`
	// SubjectModels lists the served tags the subject freezes: one model, or
	// a formation's Primary, Assistant, and Lite models.
	SubjectModels []string `json:"subject_models"`
	Lane          string   `json:"lane"`
	Suite         string   `json:"suite"`
	Cells         uint64   `json:"cells"`
	Executed      int      `json:"executed"`
	Passed        int      `json:"passed"`
	// Outcomes holds one outcome line per graded cell: lifecycle, verdict,
	// pass rate, and the failed grades.
	Outcomes  []string `json:"outcomes"`
	Verified  bool     `json:"verified"`
	Published bool     `json:"published"`
}

// quickstartCampaignID names the quickstart campaign for one subject under one
// platform release. A campaign freezes its release, so each release gets its
// own campaign and reruns under the same release reuse it.
func quickstartCampaignID(release, subject string) string {
	slug := func(value string) string {
		return strings.Trim(strings.NewReplacer(".", "-", ":", "-", "/", "-", "_", "-").Replace(strings.ToLower(value)), "-")
	}
	parts := []string{quickstartCampaignPrefix}
	if release != "" {
		parts = append(parts, slug(release))
	}
	return strings.Join(append(parts, slug(subject)), "-")
}

// quickstartSpecSuffix is a short digest of what a quickstart campaign freezes
// beyond its suite: each subject model's served tag and digest, and the judge's.
// When a re-freeze changes any of them, the quickstart moves to the campaign
// named with this suffix instead of failing on the existing campaign, and
// reruns of the same spec keep landing on that campaign.
func quickstartSpecSuffix(registry []*evalv1.ModelVariant, subjectTags []string, judge *evalv1.ModelVariant) string {
	bindings := make([]string, 0, len(subjectTags)+1)
	for _, tag := range subjectTags {
		binding := tag
		for _, variant := range registry {
			if variant.GetServedModelTag() == tag {
				binding += "@" + variant.GetModelDigest()
				break
			}
		}
		bindings = append(bindings, binding)
	}
	bindings = append(bindings, "judge="+judge.GetServedModelTag()+"@"+judge.GetModelDigest())
	return models.SHA256Hex([]byte(strings.Join(bindings, "\n")))[:8]
}

func quickstartEvalCmd(deps nativeEvalDeps) *cobra.Command {
	var opts quickstartOptions
	cmd := &cobra.Command{
		Use:   "quickstart",
		Short: "Run a one-cell governed evaluation of a light model or formation in seconds",
		Long: `Run the smallest real evaluation: the built-in quickstart suite (one Primary-only,
answer-only scenario, graded deterministically) against one light model or one
formation, as a single assignment. It is the same governed path as every
campaign: the campaign is frozen, the model is dispatched through the Inference
Operator with signed receipts, the result is graded, and the run is verified
offline. Nothing is published unless --publish is set.

The quickstart suite has no judged criteria, so no judge is dispatched; the
campaign still freezes one (the subject model, or --judge).

Reruns under the same release reuse the campaign and add a new run. When a
model digest, the formation, or the judge has changed since that campaign was
frozen, the quickstart freezes a sibling campaign named with a short digest of
the new models instead.

Examples:
  ztd eval quickstart
  ztd eval quickstart --model llama3.2:1b
  ztd eval quickstart --formation formation-lite`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runQuickstart(cmd, deps, opts)
		},
	}
	cmd.Flags().StringVar(&opts.Model, "model", quickstartDefaultModel, "Registry model (served tag) to score in the Primary role")
	cmd.Flags().StringVar(&opts.Formation, "formation", "", "Score a catalog formation instead of one model (see ztd eval formations list)")
	cmd.Flags().StringVar(&opts.Judge, "judge", "", "Registry model to freeze as the campaign judge (default: the subject model; never dispatched by the quickstart suite)")
	cmd.Flags().BoolVar(&opts.Publish, "publish", false, "Publish the run to the public mirror")
	cmd.Flags().BoolVar(&opts.RequireWitness, "require-witness", false, "Require provider-boundary observation and model-provenance witness evidence")
	cmd.MarkFlagsMutuallyExclusive("model", "formation")
	return cmd
}

func runQuickstart(cmd *cobra.Command, deps nativeEvalDeps, opts quickstartOptions) error {
	jsonOutput := output.JSONEnabled(cmd)
	cfg, fileSvc, err := nativeEvalEnvironment(cmd, deps)
	if err != nil {
		return err
	}
	registry, err := newModelInventories(fileSvc, cfg.ProjectRoot).variants(cmd.Context(), modelScopeRegistry)
	if err != nil {
		return fmt.Errorf("evaluation: quickstart: %w", err)
	}
	if len(registry) == 0 {
		return fmt.Errorf("evaluation: quickstart: the model registry is empty\nFreeze the models your Inference Operator serves with:\nztd eval models freeze\n%w", constants.ErrEvaluationSelectionEmpty)
	}
	platform := platformIdentity(cmd)
	create := campaignCreateSpec{
		Repetitions:            1,
		SuiteID:                evaluation.QuickstartSuiteID,
		VersionOnCatalogChange: true,
		Platform:               platform,
		Variants:               registry,
	}
	subject, judgeTag, lane := opts.Model, opts.Model, campaignLaneModelRole
	subjectTags := []string{opts.Model}
	if opts.Formation != "" {
		subject, lane = opts.Formation, campaignLaneSystem
		create.FormationCatalog, err = newFormationCatalog(cfg.ProjectRoot).topologies()
		if err != nil {
			return fmt.Errorf("evaluation: quickstart: formation catalog: %w", err)
		}
		create.FormationIDs = []string{opts.Formation}
		formation, err := create.FormationCatalog.Formation(opts.Formation)
		if err != nil {
			return fmt.Errorf("evaluation: quickstart: %w (see ztd eval formations list)", err)
		}
		judgeTag = formation.Primary.ServedModelTag
		subjectTags = []string{formation.Primary.ServedModelTag, formation.Assistant.ServedModelTag, formation.Lite.ServedModelTag}
	} else if create.Variants, err = (&ModelSelector{}).withArgs([]string{opts.Model}).Resolve(registry); err != nil {
		return fmt.Errorf("evaluation: quickstart: %w\nPick a served model from ztd eval models list, or pull one with ztd eval models pull %s", err, opts.Model)
	}
	if opts.Judge != "" {
		judgeTag = opts.Judge
	}
	if create.Judge, err = evaluation.ResolveCampaignJudge(registry, judgeTag); err != nil {
		return fmt.Errorf("evaluation: quickstart: judge: %w", err)
	}
	create.CampaignID = quickstartCampaignID(platform.Release, subject)
	created, err := createCampaign(cmd.Context(), deps, fileSvc, create)
	if errors.Is(err, constants.ErrEvaluationCampaignConflict) {
		create.CampaignID += "-" + quickstartSpecSuffix(registry, subjectTags, create.Judge)
		created, err = createCampaign(cmd.Context(), deps, fileSvc, create)
	}
	if err != nil {
		return err
	}
	campaignID := created.Spec.GetCampaignId()
	suite := created.Spec.GetCatalogRef().GetId() + "@" + created.Spec.GetCatalogRef().GetVersion()
	if !jsonOutput {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Quickstart: %s (%s) on %s, campaign %s\n\n", subject, lane, suite, campaignID)
	}
	result, runErr := runStartFlow(cmd, deps, runStartFlowOptions{
		CampaignID:         campaignID,
		Publish:            opts.Publish,
		Daemon:             true,
		Verify:             true,
		RequireObservation: opts.RequireWitness,
		RequireProvenance:  opts.RequireWitness,
		JSONOutput:         jsonOutput,
	})
	if errors.Is(runErr, constants.ErrInferenceOperatorNotFound) {
		return fmt.Errorf("%w\nNo Operator serves models yet. Restart the Gateway with embedded inference, for example:\nztd gw start --capabilities data,inference --inference-ollama-endpoint http://127.0.0.1:11434", runErr)
	}
	if result == nil || result.RunID == "" {
		return runErr
	}
	payload := quickstartResultJSON{
		CampaignID:    campaignID,
		RunID:         result.RunID,
		Subject:       subject,
		SubjectModels: subjectTags,
		Lane:          lane,
		Suite:         suite,
		Cells:         result.Cells,
		Executed:      result.Executed,
		Verified:      result.Report != nil && result.Report.GetFailureCount() == 0,
		Published:     opts.Publish,
	}
	store := evaluation.NewStore(fileSvc)
	assignments, err := store.ListAssignments(cmd.Context(), result.RunID)
	if err != nil {
		return fmt.Errorf("evaluation: quickstart: read results: %w", err)
	}
	for _, assignment := range assignments {
		exists, err := store.AssignmentResultExists(cmd.Context(), result.RunID, assignment.GetAssignmentId())
		if err != nil {
			return fmt.Errorf("evaluation: quickstart: read results: %w", err)
		}
		if !exists {
			continue
		}
		assignmentResult, err := store.LoadAssignmentResult(cmd.Context(), result.RunID, assignment.GetAssignmentId())
		if err != nil {
			return fmt.Errorf("evaluation: quickstart: read results: %w", err)
		}
		if cellFromResult(assignmentResult).Passed {
			payload.Passed++
		}
		payload.Outcomes = append(payload.Outcomes, assignment.GetScenarioId()+": "+evaluation.AssignmentOutcomeSummary(assignmentResult))
	}
	if jsonOutput {
		if err := output.WriteJSON(cmd.OutOrStdout(), payload); err != nil {
			return err
		}
		return runErr
	}
	writeQuickstartSummary(cmd.OutOrStdout(), payload)
	return runErr
}

// writeQuickstartSummary reports the platform outcome (the governed run's
// evidence verified offline) apart from the model outcome (its grade), so a
// model that fails its scenario is never mistaken for a broken stack.
func writeQuickstartSummary(out io.Writer, result quickstartResultJSON) {
	platform := "FAIL  evidence did not verify (see the verification lines above)"
	if result.Verified {
		platform = "PASS  governed run verified offline: signed receipts, ledger bracket, recomputed grades"
	}
	model := "FAIL"
	if result.Executed > 0 && result.Passed == result.Executed {
		model = "PASS"
	}
	published := "not published (pass --publish to send it to the public mirror)"
	if result.Published {
		published = "published to the public mirror"
	}
	_, _ = fmt.Fprintf(out, "\nPlatform  %s\n", platform)
	_, _ = fmt.Fprintf(out, "Model     %s  %d/%d cells passed (%s)\n", model, result.Passed, result.Executed, result.Subject)
	for _, outcome := range result.Outcomes {
		_, _ = fmt.Fprintf(out, "          %s\n", outcome)
	}
	_, _ = fmt.Fprintf(out, "Run       %s, %s\n", result.RunID, published)
	scenario := evaluation.QuickstartScenarioIDs()[0]
	_, _ = fmt.Fprintf(out, "\nNext:\n")
	_, _ = fmt.Fprintf(out, "  ztd eval runs trace %s --scenario %s   # what the model saw and said\n", result.RunID, scenario)
	_, _ = fmt.Fprintf(out, "  ztd eval runs assignments %s   # grades for every cell\n", result.RunID)
	if result.Lane == campaignLaneModelRole {
		_, _ = fmt.Fprintf(out, "  ztd eval quickstart --formation formation-lite   # same cell through a light three-model formation\n")
	}
	_, _ = fmt.Fprintf(out, "  ztd eval campaigns create my-screen %s --suite %s --judge %s   # the ten-scenario screen, judged by another light model\n", quickstartScreenSubject(result), evaluation.SmokeSuiteID, quickstartScreenJudge(result.SubjectModels))
	_, _ = fmt.Fprintf(out, "  ztd eval runs start my-screen --publish=false\n")
}

// quickstartScreenJudge picks the first light judge that is not a subject model.
func quickstartScreenJudge(subjectModels []string) string {
	for _, judge := range quickstartScreenJudges {
		if !slices.Contains(subjectModels, judge) {
			return judge
		}
	}
	return "<a registry model that is not a subject>"
}

// quickstartScreenSubject is the campaign-create selector that screens the same
// subject with the smoke suite.
func quickstartScreenSubject(result quickstartResultJSON) string {
	if result.Lane == campaignLaneSystem {
		return "--formations " + result.Subject
	}
	return result.Subject
}

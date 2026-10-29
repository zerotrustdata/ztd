// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package eval

import (
	"context"
	"errors"
	"fmt"
	iofs "io/fs"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/zerotrustdata/ztd/internal/cli/output"
	"github.com/zerotrustdata/ztd/internal/constants"
	complianceevidence "github.com/zerotrustdata/ztd/internal/services/compliance/evidence"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

const campaignStatusNoRuns = "no-runs"

func campaignsEvalCmd(deps nativeEvalDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "campaigns",
		Short: "Define, inspect, and archive evaluation campaigns",
		Long: `A campaign is the frozen definition of an evaluation: which models, which
scenarios, how many repetitions, and in which lane. Executions of a campaign are
runs (ztd eval runs).`,
	}
	addLeaves(cmd,
		campaignsListCmd(deps),
		campaignsShowCmd(deps),
		campaignsCreateCmd(deps),
		campaignsTagCmd(deps),
		campaignsArchiveCmd(deps),
		campaignsUnarchiveCmd(deps),
		campaignsDeleteCmd(deps),
	)
	return cmd
}

type campaignRowJSON struct {
	CampaignID          string `json:"campaign_id"`
	Lane                string `json:"lane"`
	ModelCount          int    `json:"model_count"`
	ScenarioCount       uint32 `json:"scenario_count"`
	RepetitionCount     uint32 `json:"repetition_count"`
	HoldContext         bool   `json:"hold_context"`
	ModelRegistryDigest string `json:"model_registry_digest"`
	CatalogDigest       string `json:"catalog_digest"`
	// Release is the platform release the campaign measured; ReleaseBasis says
	// whether it was recorded at freeze (digest-bound), asserted by an operator,
	// or is unknown.
	Release      string   `json:"release,omitempty"`
	ReleaseBasis string   `json:"release_basis"`
	StackCount   int      `json:"stack_count,omitempty"`
	Archived     bool     `json:"archived"`
	Status       string   `json:"status"`
	RunIDs       []string `json:"run_ids"`
}

type campaignListJSON struct {
	Campaigns []campaignRowJSON `json:"campaigns"`
}

type runMetricsCurveJSON struct {
	RunID                    string  `json:"run_id"`
	AssignmentID             string  `json:"assignment_id,omitempty"`
	ScenarioID               string  `json:"scenario_id,omitempty"`
	Repetition               uint32  `json:"repetition"`
	RetrievalHitRate         float64 `json:"retrieval_hit_rate"`
	RedundantCommandRate     float64 `json:"redundant_command_rate"`
	StepsToDiagnosis         float64 `json:"steps_to_diagnosis"`
	DiagnosisCorrectness     float64 `json:"diagnosis_correctness"`
	EvidenceBackedConclusion float64 `json:"evidence_backed_conclusion"`
}

type campaignShowJSON struct {
	campaignRowJSON
	Condition         string                      `json:"condition"`
	Suite             string                      `json:"suite"`
	Models            []string                    `json:"models"`
	Judge             string                      `json:"judge,omitempty"`
	Runs              []runRow                    `json:"runs"`
	MetricsCurve      []runMetricsCurveJSON       `json:"metrics_curve,omitempty"`
	Archive           *evaluation.ArchiveManifest `json:"archive,omitempty"`
	CellsPer          uint64                      `json:"cells_per_run"`
	CellsPerL2        uint64                      `json:"cells_per_run_l2_enforced"`
	InvestigationMode string                      `json:"investigation_mode,omitempty"`
	InvestigationID   string                      `json:"investigation_id,omitempty"`
	DataOperatorID    string                      `json:"data_operator_id,omitempty"`
}

func investigationModeLabel(investigation *evalv1.EvaluationInvestigation) string {
	if investigation == nil {
		return "legacy"
	}
	switch investigation.GetMode() {
	case evalv1.EvaluationInvestigationMode_EVALUATION_INVESTIGATION_MODE_FRESH:
		return "fresh"
	case evalv1.EvaluationInvestigationMode_EVALUATION_INVESTIGATION_MODE_PERSISTENT:
		return "persistent"
	default:
		return "unspecified"
	}
}

func investigationLabel(show campaignShowJSON) string {
	if show.InvestigationID == "" {
		return show.InvestigationMode
	}
	return show.InvestigationMode + ":" + show.InvestigationID
}

// campaignLane reports the lane of a campaign from its frozen stack set: a
// campaign with stacks is a system-lane campaign.
func campaignLane(ctx context.Context, store *evaluation.Store, campaignID string) (string, int, error) {
	stackSet, err := store.LoadHeterogeneousStackSet(ctx, campaignID)
	if err != nil {
		if isMissingRecord(err) {
			return campaignLaneModelRole, 0, nil
		}
		return "", 0, err
	}
	return campaignLaneSystem, len(stackSet.Stacks), nil
}

func isMissingRecord(err error) bool {
	return errors.Is(err, constants.ErrNotFound) || errors.Is(err, iofs.ErrNotExist)
}

func campaignRunRows(ctx context.Context, fileSvc fs.RuntimeFileService, runIDs []string, live evaluation.LeaseLiveness, deps nativeEvalDeps) ([]runRow, error) {
	rows := make([]runRow, 0, len(runIDs))
	for _, runID := range runIDs {
		store, archived, err := evaluation.LocateRun(ctx, fileSvc, runID)
		if err != nil {
			return nil, err
		}
		row, _, err := summarizeRun(ctx, store, archived, runID, live, deps.now, adaptNewID(deps.newID))
		if err != nil {
			return nil, err
		}
		rows = append(rows, *row)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].StartedAt < rows[j].StartedAt })
	return rows, nil
}

func latestRunStatus(rows []runRow) string {
	if len(rows) == 0 {
		return campaignStatusNoRuns
	}
	return rows[len(rows)-1].Status
}

func campaignsListCmd(deps nativeEvalDeps) *cobra.Command {
	var statusFilter string
	var includeArchived bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List campaigns",
		Long: `List campaigns with the status of their latest run.

Archived campaigns are hidden unless --archived is given.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, fileSvc, err := nativeEvalEnvironment(cmd, deps)
			if err != nil {
				return err
			}
			control, err := deps.runControl.process(fileSvc)
			if err != nil {
				return err
			}
			live := leaseLiveness(control)
			type source struct {
				store    *evaluation.Store
				archived bool
			}
			sources := []source{{store: evaluation.NewStore(fileSvc)}}
			if includeArchived {
				sources = append(sources, source{store: evaluation.NewArchivedStore(fileSvc), archived: true})
			}
			rows := make([]campaignRowJSON, 0)
			for _, src := range sources {
				entries, err := src.store.ListCampaigns(cmd.Context())
				if err != nil {
					return fmt.Errorf("evaluation: campaigns list: %w", err)
				}
				for _, entry := range entries {
					lane, stacks, err := campaignLane(cmd.Context(), src.store, entry.CampaignID)
					if err != nil {
						return fmt.Errorf("evaluation: campaigns list: %w", err)
					}
					runRows, err := campaignRunRows(cmd.Context(), fileSvc, entry.RunIDs, live, deps)
					if err != nil {
						return fmt.Errorf("evaluation: campaigns list: %w", err)
					}
					release, err := src.store.LoadCampaignRelease(cmd.Context(), entry.CampaignID)
					if err != nil {
						return fmt.Errorf("evaluation: campaigns list: %w", err)
					}
					status := latestRunStatus(runRows)
					if statusFilter != "" && status != statusFilter {
						continue
					}
					rows = append(rows, campaignRowJSON{
						CampaignID:          entry.CampaignID,
						Lane:                lane,
						ModelCount:          entry.ModelCount,
						ScenarioCount:       entry.ScenarioCount,
						RepetitionCount:     entry.RepetitionCount,
						ModelRegistryDigest: entry.ModelRegistryDigest,
						CatalogDigest:       entry.CatalogDigest,
						Release:             release.Release,
						ReleaseBasis:        string(release.Basis),
						StackCount:          stacks,
						Archived:            src.archived,
						Status:              status,
						RunIDs:              append([]string{}, entry.RunIDs...),
					})
				}
			}
			if output.JSONEnabled(cmd) {
				return output.WriteJSON(cmd.OutOrStdout(), campaignListJSON{Campaigns: rows})
			}
			if len(rows) == 0 {
				cmd.Println("No campaigns found")
				return nil
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "CAMPAIGN\tRELEASE\tLANE\tMODELS\tSCENARIOS\tREPS\tRUNS\tSTATUS\tARCHIVED")
			for _, row := range rows {
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%d\t%d\t%s\t%t\n",
					row.CampaignID, releaseLabel(row.Release, row.ReleaseBasis), row.Lane, row.ModelCount, row.ScenarioCount, row.RepetitionCount, len(row.RunIDs), row.Status, row.Archived)
			}
			return w.Flush()
		},
	}
	cmd.Flags().StringVar(&statusFilter, "status", "", "Only campaigns whose latest run has this status (for example verified, running, no-runs)")
	cmd.Flags().BoolVar(&includeArchived, "archived", false, "Include archived campaigns")
	return cmd
}

func campaignsShowCmd(deps nativeEvalDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "show <campaign>",
		Short: "Show one campaign and its runs",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			campaignID := args[0]
			_, fileSvc, err := nativeEvalEnvironment(cmd, deps)
			if err != nil {
				return err
			}
			control, err := deps.runControl.process(fileSvc)
			if err != nil {
				return err
			}
			store, archived, err := evaluation.LocateCampaign(cmd.Context(), fileSvc, campaignID)
			if err != nil {
				return fmt.Errorf("evaluation: campaigns show: %w", err)
			}
			spec, err := store.LoadCampaignSpec(cmd.Context(), campaignID)
			if err != nil {
				return fmt.Errorf("evaluation: campaigns show: %w", err)
			}
			lane, stacks, err := campaignLane(cmd.Context(), store, campaignID)
			if err != nil {
				return fmt.Errorf("evaluation: campaigns show: %w", err)
			}
			runIDs, err := campaignRunIDs(cmd.Context(), fileSvc, store, archived, campaignID)
			if err != nil {
				return fmt.Errorf("evaluation: campaigns show: %w", err)
			}
			runRows, err := campaignRunRows(cmd.Context(), fileSvc, runIDs, leaseLiveness(control), deps)
			if err != nil {
				return fmt.Errorf("evaluation: campaigns show: %w", err)
			}
			release, err := store.LoadCampaignRelease(cmd.Context(), campaignID)
			if err != nil {
				return fmt.Errorf("evaluation: campaigns show: %w", err)
			}
			catalog, err := store.LoadScenarioCatalog(cmd.Context(), campaignID)
			if err != nil {
				return fmt.Errorf("evaluation: campaigns show: %w", err)
			}
			payload := campaignShowJSON{
				campaignRowJSON: campaignRowJSON{
					CampaignID:          campaignID,
					Lane:                lane,
					ModelCount:          len(spec.GetModelRegistry()),
					ScenarioCount:       spec.GetScenarioCount(),
					RepetitionCount:     spec.GetRepetitionCount(),
					HoldContext:         spec.GetHoldContext(),
					ModelRegistryDigest: spec.GetModelRegistryDigest(),
					CatalogDigest:       spec.GetCatalogDigest(),
					Release:             release.Release,
					ReleaseBasis:        string(release.Basis),
					StackCount:          stacks,
					Archived:            archived,
					Status:              latestRunStatus(runRows),
					RunIDs:              runIDs,
				},
				Condition:         evaluation.CampaignCondition(spec),
				Suite:             spec.GetCatalogRef().GetId() + "@" + spec.GetCatalogRef().GetVersion(),
				Judge:             spec.GetJudgeVariant().GetServedModelTag(),
				Runs:              runRows,
				MetricsCurve:      extractCampaignMetricsCurve(cmd.Context(), store, runIDs),
				CellsPer:          campaignCells(catalog, spec, lane, stacks, evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE),
				CellsPerL2:        campaignCells(catalog, spec, lane, stacks, evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_CONSENSUS),
				InvestigationMode: investigationModeLabel(spec.GetInvestigation()),
				InvestigationID:   spec.GetInvestigation().GetInvestigationId(),
				DataOperatorID:    spec.GetInvestigation().GetDataOperatorId(),
			}
			for _, variant := range spec.GetModelRegistry() {
				payload.Models = append(payload.Models, variant.GetServedModelTag())
			}
			if archived {
				if payload.Archive, err = evaluation.LoadArchiveManifest(cmd.Context(), fileSvc, evaluation.ArchiveKindCampaign, campaignID); err != nil {
					return fmt.Errorf("evaluation: campaigns show: %w", err)
				}
			}
			if output.JSONEnabled(cmd) {
				return output.WriteJSON(cmd.OutOrStdout(), payload)
			}
			out := cmd.OutOrStdout()
			_, _ = fmt.Fprintf(out, "Campaign: %s\nCondition: %s\nRelease: %s\nLane: %s\nSuite: %s\nModels: %d (%s)\nScenarios: %d\nRepetitions: %d\nHold context: %t\nInvestigation: %s\nCells per run (doctrine/ratify): %d\nCells per run (consensus/notary): %d\nModel registry digest: %s\nCatalog digest: %s\nStatus: %s\nArchived: %t\n",
				campaignID, payload.Condition, releaseLabel(release.Release, string(release.Basis)), lane, payload.Suite, payload.ModelCount, strings.Join(payload.Models, ", "), payload.ScenarioCount, payload.RepetitionCount,
				payload.HoldContext, investigationLabel(payload), payload.CellsPer, payload.CellsPerL2, payload.ModelRegistryDigest, payload.CatalogDigest, payload.Status, archived)
			if payload.InvestigationID != "" {
				_, _ = fmt.Fprintf(out, "Investigation ID: %s\nData Operator ID: %s\n", payload.InvestigationID, payload.DataOperatorID)
			}
			if payload.Judge != "" {
				_, _ = fmt.Fprintf(out, "Judge: %s\n", payload.Judge)
			}
			if stacks > 0 {
				_, _ = fmt.Fprintf(out, "Stacks: %d\n", stacks)
			}
			if payload.Archive != nil {
				_, _ = fmt.Fprintf(out, "Archived at: %s by %s\n", payload.Archive.ArchivedAt.Format("2006-01-02T15:04:05Z"), payload.Archive.ArchivedBy)
			}
			if len(runRows) == 0 {
				_, _ = fmt.Fprintln(out, "Runs: none")
				return nil
			}
			w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(w, "RUN\tSTATUS\tPROGRESS\tSTARTED")
			for _, row := range runRows {
				_, _ = fmt.Fprintf(w, "%s\t%s\t%d/%d\t%s\n", row.RunID, row.Status, row.Terminal, row.ExpectedAssignments, row.StartedAt)
			}
			if err := w.Flush(); err != nil {
				return err
			}
			if len(payload.MetricsCurve) > 0 {
				_, _ = fmt.Fprintln(out, "\nLongitudinal Metrics Curve:")
				mw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
				_, _ = fmt.Fprintln(mw, "RUN\tREP\tSCENARIO\tRETRIEVAL\tREDUNDANT\tSTEPS\tCORRECT\tEVIDENCE-BACKED")
				for _, pt := range payload.MetricsCurve {
					_, _ = fmt.Fprintf(mw, "%s\t%d\t%s\t%.1f%%\t%.1f%%\t%.0f\t%.0f%%\t%.0f%%\n",
						pt.RunID, pt.Repetition, pt.ScenarioID, pt.RetrievalHitRate*100, pt.RedundantCommandRate*100, pt.StepsToDiagnosis, pt.DiagnosisCorrectness*100, pt.EvidenceBackedConclusion*100)
				}
				return mw.Flush()
			}
			return nil
		},
	}
}

func extractCampaignMetricsCurve(ctx context.Context, store *evaluation.Store, runIDs []string) []runMetricsCurveJSON {
	var curve []runMetricsCurveJSON
	for _, runID := range runIDs {
		assignments, err := store.ListAssignments(ctx, runID)
		if err != nil || len(assignments) == 0 {
			continue
		}
		results, err := store.LoadAssignmentResults(ctx, runID, assignments)
		if err != nil {
			continue
		}
		for _, assignment := range assignments {
			result, ok := results[assignment.GetAssignmentId()]
			if !ok || result == nil || len(result.GetDecomposedScores()) == 0 {
				continue
			}
			scoreMap := make(map[string]float64)
			for _, score := range result.GetDecomposedScores() {
				scoreMap[score.GetDimension()] = score.GetValue()
			}
			if _, hasMetrics := scoreMap[evaluation.MetricStepsToDiagnosis]; hasMetrics {
				curve = append(curve, runMetricsCurveJSON{
					RunID:                    runID,
					AssignmentID:             assignment.GetAssignmentId(),
					ScenarioID:               assignment.GetScenarioId(),
					Repetition:               assignment.GetRepetition(),
					RetrievalHitRate:         scoreMap[evaluation.MetricRetrievalHitRate],
					RedundantCommandRate:     scoreMap[evaluation.MetricRedundantCommandRate],
					StepsToDiagnosis:         scoreMap[evaluation.MetricStepsToDiagnosis],
					DiagnosisCorrectness:     scoreMap[evaluation.MetricDiagnosisCorrectness],
					EvidenceBackedConclusion: scoreMap[evaluation.MetricEvidenceBackedConclusion],
				})
			}
		}
	}
	return curve
}

// campaignRunIDs lists every run of a campaign, active and archived.
func campaignRunIDs(ctx context.Context, fileSvc fs.RuntimeFileService, store *evaluation.Store, archived bool, campaignID string) ([]string, error) {
	runIDs, err := store.ListCampaignRunIDs(ctx, campaignID)
	if err != nil {
		return nil, err
	}
	if archived {
		return runIDs, nil
	}
	archivedIDs, err := evaluation.NewArchivedRunStore(fileSvc).ListCampaignRunIDs(ctx, campaignID)
	if err != nil {
		return nil, err
	}
	runIDs = append(runIDs, archivedIDs...)
	sort.Strings(runIDs)
	return runIDs, nil
}

// campaignCells is the assignment count of one run, sized from the catalog the
// campaign froze rather than from any built-in suite.
func campaignCells(catalog *evalv1.EvaluationScenarioCatalog, spec *evalv1.EvaluationCampaignSpec, lane string, stacks int, posture evalv1.EvaluationGovernancePosture) uint64 {
	if lane == campaignLaneSystem {
		return evaluation.FormationMatrixSize(catalog, uint64(stacks), posture)
	}
	return evaluation.ModelRoleMatrixSize(catalog, uint64(len(spec.GetModelRegistry())), spec.GetRepetitionCount(), posture)
}

// campaignCreateSpec is everything that defines one campaign.
type campaignCreateSpec struct {
	CampaignID       string
	Variants         []*evalv1.ModelVariant
	FormationIDs     []string
	FormationCatalog *evaluation.ExecutionTopologies
	Repetitions      uint32
	Investigation    *evalv1.EvaluationInvestigation
	// HoldContext carries each cell's investigation into its next repetition.
	HoldContext bool
	// SuiteID names the suite the campaign freezes; empty means the default suite.
	SuiteID string
	// VersionOnCatalogChange freezes a new campaign, under CampaignID plus the
	// catalog digest, when CampaignID already froze a different catalog. Without
	// it that case is a frozen-spec conflict. The resolved ID is on the result's
	// Spec.
	VersionOnCatalogChange bool
	// Platform is the build identity the campaign freezes under.
	Platform evaluation.PlatformIdentity
	// Judge is the registry model, resolved by evaluation.ResolveCampaignJudge,
	// that scores the campaign's judged criteria.
	Judge *evalv1.ModelVariant
}

type campaignCreateResult struct {
	Spec     *evalv1.EvaluationCampaignSpec
	Catalog  *evalv1.EvaluationScenarioCatalog
	StackSet *evaluation.HeterogeneousStackSet
}

// createCampaign freezes a model-role campaign: its model registry, scenario
// catalog and artifacts. The per-campaign model inventory is written as an
// internal artifact. Creating a campaign whose ID
// already holds the same frozen spec is a no-op.
func createCampaign(ctx context.Context, deps nativeEvalDeps, fileSvc fs.RuntimeFileService, spec campaignCreateSpec) (*campaignCreateResult, error) {
	if err := complianceevidence.ValidatePathElement(spec.CampaignID); err != nil {
		return nil, fmt.Errorf("evaluation: campaigns create: campaign ID: %w", err)
	}
	store := evaluation.NewStore(fileSvc)
	suiteID := spec.SuiteID
	if suiteID == "" {
		suiteID = evaluation.DefaultSuiteID
	}
	catalog, artifacts, err := store.LoadSuiteCatalog(ctx, suiteID)
	if err != nil {
		return nil, fmt.Errorf("evaluation: campaigns create: %w", err)
	}
	if spec.VersionOnCatalogChange {
		spec.CampaignID, err = evaluation.ResolveCampaignIDForCatalog(ctx, store, spec.CampaignID, catalog.GetCatalogDigest())
		if err != nil {
			return nil, fmt.Errorf("evaluation: campaigns create: %w", err)
		}
	}
	if _, archived, err := evaluation.LocateCampaign(ctx, fileSvc, spec.CampaignID); err == nil && archived {
		return nil, fmt.Errorf("evaluation: campaigns create: campaign %q: %w", spec.CampaignID, constants.ErrEvaluationArchived)
	} else if err != nil && !errors.Is(err, constants.ErrNotFound) {
		return nil, err
	}
	variants := spec.Variants
	if len(spec.FormationIDs) > 0 {
		variants, err = evaluation.MaterializeFormationVariants(variants, spec.FormationIDs, spec.FormationCatalog)
		if err != nil {
			return nil, fmt.Errorf("evaluation: campaigns create: formation variants: %w", err)
		}
	}
	freeze, err := evaluation.MaterializeModelRegistry(spec.CampaignID, variants)
	if err != nil {
		return nil, fmt.Errorf("evaluation: campaigns create: %w", err)
	}
	if err := evaluation.ValidateModelRegistry(freeze); err != nil {
		return nil, fmt.Errorf("evaluation: campaigns create: %w", err)
	}
	if len(spec.FormationIDs) == 0 && len(freeze.Variants) != 1 {
		return nil, fmt.Errorf("evaluation: campaigns create: %d models selected: %w", len(freeze.Variants), constants.ErrEvaluationCampaignSubjectInvalid)
	}
	var stackSet *evaluation.HeterogeneousStackSet
	if len(spec.FormationIDs) > 0 {
		stackSet, err = evaluation.GenerateFormationCatalogStackSet(evaluation.FormationCatalogStackGenerationRequest{
			CampaignID:   spec.CampaignID,
			Variants:     freeze.Variants,
			FormationIDs: spec.FormationIDs,
			Catalog:      spec.FormationCatalog,
		})
		if err != nil {
			return nil, fmt.Errorf("evaluation: campaigns create: formations: %w", err)
		}
	}
	controller := evaluation.NewCampaignController(store, nil, deps.now, adaptNewID(deps.newID))
	campaignSpec, err := controller.CreateCampaign(ctx, evaluation.CampaignCreateRequest{
		CampaignID:        spec.CampaignID,
		Catalog:           catalog,
		Inventory:         freeze,
		ScenarioArtifacts: artifacts,
		RepetitionCount:   spec.Repetitions,
		HoldContext:       spec.HoldContext,
		Platform:          spec.Platform,
		Investigation:     spec.Investigation,
		FormationStackSet: stackSet,
		FormationCatalog:  spec.FormationCatalog,
		Judge:             spec.Judge,
	})
	if err != nil {
		return nil, fmt.Errorf("evaluation: campaigns create: %w", err)
	}
	if _, _, err := evaluation.MaterializeCampaignInventory(evaluation.MaterializeCampaignInventoryRequest{
		Context:     ctx,
		FileService: fileSvc,
		CampaignID:  spec.CampaignID,
		Variants:    freeze.Variants,
	}); err != nil {
		return nil, fmt.Errorf("evaluation: campaigns create: %w", err)
	}
	return &campaignCreateResult{Spec: campaignSpec, Catalog: catalog, StackSet: stackSet}, nil
}

type campaignCreateJSON struct {
	CampaignID          string `json:"campaign_id"`
	Condition           string `json:"condition"`
	Lane                string `json:"lane"`
	Suite               string `json:"suite"`
	ModelCount          int    `json:"model_count"`
	ScenarioCount       uint32 `json:"scenario_count"`
	RepetitionCount     uint32 `json:"repetition_count"`
	HoldContext         bool   `json:"hold_context"`
	CellsPerRun         uint64 `json:"cells_per_run"`
	CellsPerRunL2       uint64 `json:"cells_per_run_l2_enforced"`
	ModelRegistryDigest string `json:"model_registry_digest"`
	CatalogDigest       string `json:"catalog_digest"`
	StackCount          int    `json:"stack_count,omitempty"`
	Judge               string `json:"judge"`
}

func campaignsCreateCmd(deps nativeEvalDeps) *cobra.Command {
	var reps uint32
	var holdContext bool
	var condition string
	var suite string
	var judgeTag string
	var investigationMode string
	var dataOperatorID string
	var formationIDs []string
	var selector ModelSelector
	cmd := &cobra.Command{
		Use:   "create <campaign> [model-selector]",
		Short: "Freeze a model-role or formation campaign",
		Long: `Freeze a campaign: the selected registry models, the suite's scenario catalog,
and the repetition count. Nothing executes until a run starts (ztd eval runs
start). A campaign freezes no governance: each run freezes the live Gateway's
posture and consensus binding when it starts.

--condition declares the operational condition: "longitudinal" (runs sequential
repetitions on one persistent investigation, restoring the workspace each time),
"control" (runs fresh investigations for comparison), or "held-context" (replays
conversation and leaves workspace dirty across repetitions).

--suite picks the suite whose scenarios the campaign scores (default:
default-suite; see ztd eval suites). The suite is copied into the campaign, so
editing or deleting it later never changes the campaign.

--investigation freezes the scored investigation scope. Use "fresh" (the
default) to give each assignment its own investigation, or
"persistent:<investigation-id>" to reuse one investigation across runs. A
persistent investigation requires --data-operator to name the stable Operator
identity whose authoritative ledger owns that investigation. The longitudinal
condition selects a persistent investigation and therefore also requires
--data-operator.

With --reps greater than 1, repetitions run sequentially and, unless a
--condition is given, each repetition continues the previous repetition's
conversation and workspace. Pass --hold-context=false to start every
repetition fresh.

--judge names the registry model that scores every judged criterion, whatever
model or formation an assignment executes, so no subject grades itself. Its
digest is frozen from the model registry into the campaign.

A model-role campaign takes a model selector (positional models, --family,
--max-params, or --all) and freezes exactly one model. A system campaign takes
one or more catalog formation IDs with --formations and freezes the registered
models required by those formations. Formation campaigns currently support one
repetition without held context. The selectors are mutually exclusive.

Qualify several model-role candidates with ztd eval rollout, which runs one
campaign per model.

Examples:
  ztd eval campaigns create eval-qwen-held qwen3:4b --reps 3
  ztd eval campaigns create eval-qwen-fresh qwen3:4b --reps 3 --hold-context=false
  ztd eval campaigns create eval-qwen35 qwen3.5:4b
  ztd eval campaigns create eval-custom qwen3:4b --suite my-suite
  ztd eval campaigns create eval-system --formations formation-01
  ztd eval campaigns create eval-system-pair --formations formation-01,formation-02
  ztd eval campaigns create eval-repeat qwen3:4b --investigation persistent:checkout-incident --data-operator op-123
  ztd eval campaigns create eval-longitudinal qwen3:4b --condition longitudinal --reps 3 --data-operator op-123`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			campaignID := args[0]
			selector = *selector.withArgs(args[1:])
			formationIDs = normalizeFormationIDs(formationIDs)
			formationsSet := cmd.Flags().Changed("formations")
			if formationsSet && selector.IsSet() {
				return fmt.Errorf("evaluation: campaigns create: --formations cannot be combined with model selectors: %w", constants.ErrEvaluationFlagsInvalid)
			}
			if formationsSet && len(formationIDs) == 0 {
				return fmt.Errorf("evaluation: campaigns create: --formations requires at least one catalog ID: %w", constants.ErrEvaluationSelectionEmpty)
			}
			condition = strings.ToLower(strings.TrimSpace(condition))
			var err error
			investigationMode, holdContext, err = applyCampaignCondition(condition, campaignID, investigationMode, dataOperatorID, holdContext)
			if err != nil {
				return err
			}
			investigation, err := parseCampaignInvestigation(investigationMode, dataOperatorID)
			if err != nil {
				return err
			}
			if !cmd.Flags().Changed("hold-context") && condition == "" {
				holdContext = reps > 1
			} else if holdContext && reps <= 1 {
				return fmt.Errorf("evaluation: campaigns create: --hold-context applies only with --reps greater than 1: %w", constants.ErrEvaluationFlagsInvalid)
			}
			if formationsSet && (reps != 1 || holdContext) {
				return fmt.Errorf("evaluation: campaigns create: formation campaigns currently require one repetition without held context: %w", constants.ErrEvaluationFlagsInvalid)
			}
			if !formationsSet && !selector.IsSet() {
				return fmt.Errorf("evaluation: campaigns create: name a model, or pass --family, --max-params, or --all: %w", constants.ErrEvaluationSelectionEmpty)
			}
			cfg, fileSvc, err := nativeEvalEnvironment(cmd, deps)
			if err != nil {
				return err
			}
			registry, err := newModelInventories(fileSvc, cfg.ProjectRoot).variants(cmd.Context(), modelScopeRegistry)
			if err != nil {
				return fmt.Errorf("evaluation: campaigns create: %w", err)
			}
			if len(registry) == 0 {
				return fmt.Errorf("evaluation: campaigns create: the model registry is empty\nFreeze or import models with one of:\nztd eval models freeze\nztd eval models import\n%w", constants.ErrEvaluationSelectionEmpty)
			}
			judge, err := evaluation.ResolveCampaignJudge(registry, judgeTag)
			if err != nil {
				return fmt.Errorf("evaluation: campaigns create: --judge: %w", err)
			}
			create := campaignCreateSpec{CampaignID: campaignID, Repetitions: reps, HoldContext: holdContext, SuiteID: suite, Platform: platformIdentity(cmd), Investigation: investigation, Judge: judge}
			if formationsSet {
				create.FormationCatalog, err = newFormationCatalog(cfg.ProjectRoot).topologies()
				if err != nil {
					return fmt.Errorf("evaluation: campaigns create: formation catalog: %w", err)
				}
				create.FormationIDs = formationIDs
				create.Variants = registry
			} else if create.Variants, err = selector.Resolve(registry); err != nil {
				return fmt.Errorf("evaluation: campaigns create: %w", err)
			}
			result, err := createCampaign(cmd.Context(), deps, fileSvc, create)
			if err != nil {
				return err
			}
			lane, stackCount := campaignLaneModelRole, 0
			if result.StackSet != nil {
				lane, stackCount = campaignLaneSystem, len(result.StackSet.Stacks)
			}
			payload := campaignCreateJSON{
				CampaignID:          campaignID,
				Condition:           evaluation.CampaignCondition(result.Spec),
				Lane:                lane,
				Suite:               result.Spec.GetCatalogRef().GetId() + "@" + result.Spec.GetCatalogRef().GetVersion(),
				ModelCount:          len(result.Spec.GetModelRegistry()),
				ScenarioCount:       result.Spec.GetScenarioCount(),
				RepetitionCount:     result.Spec.GetRepetitionCount(),
				HoldContext:         result.Spec.GetHoldContext(),
				CellsPerRun:         campaignCells(result.Catalog, result.Spec, lane, stackCount, evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE),
				CellsPerRunL2:       campaignCells(result.Catalog, result.Spec, lane, stackCount, evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_CONSENSUS),
				ModelRegistryDigest: result.Spec.GetModelRegistryDigest(),
				CatalogDigest:       result.Spec.GetCatalogDigest(),
				StackCount:          stackCount,
				Judge:               result.Spec.GetJudgeVariant().GetServedModelTag(),
			}
			if output.JSONEnabled(cmd) {
				return output.WriteJSON(cmd.OutOrStdout(), payload)
			}
			out := cmd.OutOrStdout()
			_, _ = fmt.Fprintf(out, "Campaign created\nCampaign: %s\nCondition: %s\nLane: %s\nSuite: %s\nModels: %d\nScenarios: %d\nRepetitions: %d\nHold context: %t\nCells per run (doctrine/ratify): %d\nCells per run (consensus/notary): %d\nJudge: %s\nModel registry digest: %s\nCatalog digest: %s\n",
				payload.CampaignID, payload.Condition, payload.Lane, payload.Suite, payload.ModelCount, payload.ScenarioCount, payload.RepetitionCount, payload.HoldContext, payload.CellsPerRun, payload.CellsPerRunL2, payload.Judge, payload.ModelRegistryDigest, payload.CatalogDigest)
			if stackCount > 0 {
				_, _ = fmt.Fprintf(out, "Formation stacks: %d\n", stackCount)
			}
			_, err = fmt.Fprintf(out, "\nNext, start the run with:\nztd eval runs start %s\n", campaignID)
			return err
		},
	}
	cmd.Flags().Uint32Var(&reps, "reps", 1, "Repetitions of each matrix cell")
	cmd.Flags().BoolVar(&holdContext, "hold-context", false, "With --reps > 1, continue each repetition from the previous repetition's conversation and workspace (default true when --reps > 1 and no --condition is given)")
	cmd.Flags().StringVar(&condition, "condition", "", "Operational condition: longitudinal (persistent sequential runs), control (fresh investigation control), or held-context")
	cmd.Flags().StringVar(&suite, "suite", evaluation.DefaultSuiteID, "Suite to freeze into the campaign (see ztd eval suites list)")
	cmd.Flags().StringVar(&investigationMode, "investigation", "fresh", "Investigation scope: fresh or persistent:<investigation-id>")
	cmd.Flags().StringVar(&dataOperatorID, "data-operator", "", "Stable Data Operator ID required for a persistent investigation")
	cmd.Flags().StringSliceVar(&formationIDs, "formations", nil, "Create a system-lane campaign from catalog formation IDs (comma-separated or repeat the flag)")
	cmd.Flags().StringVar(&judgeTag, "judge", evaluation.DefaultJudgeServedModelTag, "Registry model (served tag) that judges every assignment in the campaign")
	selector.bindFlags(cmd)
	return cmd
}

func applyCampaignCondition(condition, campaignID, investigationMode, dataOperatorID string, holdContext bool) (string, bool, error) {
	investigationMode = strings.TrimSpace(investigationMode)
	switch condition {
	case "":
		// Keep investigationMode and holdContext from flags.
	case "longitudinal", "persistent":
		if investigationMode == "fresh" {
			if strings.TrimSpace(dataOperatorID) == "" {
				return "", false, fmt.Errorf("evaluation: campaigns create: longitudinal condition requires --data-operator: %w", constants.ErrEvaluationFlagsInvalid)
			}
			investigationMode = "persistent:" + campaignID + "-inv"
		}
		holdContext = false
	case "control", "fresh":
		investigationMode = "fresh"
		holdContext = false
	case "held-context", "held_context":
		holdContext = true
	default:
		return "", false, fmt.Errorf("evaluation: campaigns create: unknown --condition %q (expected longitudinal, control, or held-context): %w", condition, constants.ErrEvaluationFlagsInvalid)
	}
	return investigationMode, holdContext, nil
}

func normalizeFormationIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	result := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result
}

func parseCampaignInvestigation(mode, dataOperatorID string) (*evalv1.EvaluationInvestigation, error) {
	mode = strings.TrimSpace(mode)
	dataOperatorID = strings.TrimSpace(dataOperatorID)
	var investigation *evalv1.EvaluationInvestigation
	switch {
	case mode == "fresh":
		if dataOperatorID != "" {
			return nil, fmt.Errorf("evaluation: campaigns create: --data-operator applies only to a persistent investigation: %w", constants.ErrEvaluationFlagsInvalid)
		}
		investigation = &evalv1.EvaluationInvestigation{Mode: evalv1.EvaluationInvestigationMode_EVALUATION_INVESTIGATION_MODE_FRESH}
	case strings.HasPrefix(mode, "persistent:"):
		investigationID := strings.TrimPrefix(mode, "persistent:")
		if investigationID == "" || dataOperatorID == "" {
			return nil, fmt.Errorf("evaluation: campaigns create: persistent investigations require an ID and --data-operator: %w", constants.ErrEvaluationFlagsInvalid)
		}
		investigation = &evalv1.EvaluationInvestigation{
			Mode:            evalv1.EvaluationInvestigationMode_EVALUATION_INVESTIGATION_MODE_PERSISTENT,
			InvestigationId: investigationID,
			DataOperatorId:  dataOperatorID,
		}
	default:
		return nil, fmt.Errorf("evaluation: campaigns create: --investigation must be fresh or persistent:<investigation-id>: %w", constants.ErrEvaluationFlagsInvalid)
	}
	if err := evaluation.ValidateCampaignInvestigation(investigation); err != nil {
		return nil, fmt.Errorf("evaluation: campaigns create: %w", err)
	}
	return investigation, nil
}

// releaseLabel renders a campaign's release with how it is known: a recorded
// release is digest-bound, an asserted one is an operator tag.
func releaseLabel(release, basis string) string {
	if release == "" {
		return string(evaluation.ReleaseBasisUnknown)
	}
	return release + " (" + basis + ")"
}

func campaignsTagCmd(deps nativeEvalDeps) *cobra.Command {
	var release string
	cmd := &cobra.Command{
		Use:   "tag --release <vX.Y.Z> <campaign>...",
		Short: "Assert the platform release of campaigns frozen before releases were recorded",
		Long: `Campaigns frozen by ztd v1.0.0 or later record the platform release in their
digest. Campaigns frozen by earlier development builds, numbered v2.x before
ztd v1.0.0, recorded none, so their release is unknown until an operator
asserts it. The tag is stored beside the campaign, outside its
digest, and is shown as "(asserted)" rather than "(recorded)".

A campaign that recorded its release cannot be tagged: the recorded release is
digest-bound. Re-tagging an asserted campaign replaces the earlier assertion.

Examples:
  ztd eval campaigns tag --release v2.2.7 eval-qwen eval-gemma`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(release) == "" {
				return fmt.Errorf("evaluation: campaigns tag: --release is required: %w", constants.ErrEvaluationFlagsInvalid)
			}
			_, fileSvc, err := nativeEvalEnvironment(cmd, deps)
			if err != nil {
				return err
			}
			for _, campaignID := range args {
				store, _, err := evaluation.LocateCampaign(cmd.Context(), fileSvc, campaignID)
				if err != nil {
					return fmt.Errorf("evaluation: campaigns tag: %w", err)
				}
				if err := store.TagCampaignRelease(cmd.Context(), campaignID, release, deps.now()); err != nil {
					return fmt.Errorf("evaluation: campaigns tag: %w", err)
				}
				if !output.JSONEnabled(cmd) {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Tagged campaign %s as %s (asserted)\n", campaignID, release)
				}
			}
			if output.JSONEnabled(cmd) {
				return output.WriteJSON(cmd.OutOrStdout(), campaignTagJSON{Release: release, Campaigns: args})
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&release, "release", "", "Platform release to assert, for example v2.2.7")
	return cmd
}

type campaignTagJSON struct {
	Release   string   `json:"release"`
	Campaigns []string `json:"campaigns"`
}

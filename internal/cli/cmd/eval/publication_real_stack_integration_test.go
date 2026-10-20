// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package eval

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/gwremote"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

// These tests replace the publication tests in internal/services/evaluation that
// drove a recording or stub feed exporter and proof publisher. Every assertion
// here reads what the real Gateway-owned public mirror serves, through the
// production gwremote bootstrap reader, or the eval CLI publish commands. Batch
// counts and call order are not observable on a real mirror, so those tests
// assert the end state the batches and calls produce.

// publicationProjection is one projection the real public mirror serves.
type publicationProjection struct {
	sequence int64
	key      string
	raw      string
	record   []byte
}

// publicationSummary is the subset of an evaluation_summary the mirror serves.
type publicationSummary struct {
	sequence      int64
	Kind          string                       `json:"kind"`
	RunStatus     string                       `json:"run_status"`
	VerifierState string                       `json:"verifier_state"`
	Metadata      *publicationVerificationMeta `json:"verification_metadata"`
}

type publicationVerificationMeta struct {
	Provenance       string `json:"provenance"`
	VerifierState    string `json:"verifier_state"`
	ReportDigest     string `json:"report_digest"`
	PopulationDigest string `json:"population_digest"`
}

var publishedCountPattern = regexp.MustCompile(`Published (\d+) public projection`)
var sha256HexPattern = regexp.MustCompile(`[0-9a-f]{64}`)

// newPublicationEnv starts the real stack with its public mirror enabled and
// points the gwremote mirror readers at that mirror. The Ollama provider answers
// each turn with the scripted replies.
func newPublicationEnv(t *testing.T, replies []fixtures.OllamaReply) (*runEnv, *realStackRun) {
	t.Helper()
	control := newFakeProcessControl()
	run := newRealStackEnvironment(t, fixtures.EvalStackOptions{Posture: constants.PostureDoctrine, Replies: replies, EnablePublicSpectator: true}, testRunControl(control))
	cmd := &cobra.Command{}
	cmd.SetContext(run.ctx)
	cmd.Flags().String("project-root", run.root, "")
	data, err := run.stack.Data.Workload()
	require.NoError(t, err)
	inference, err := run.stack.Inference.Workload()
	require.NoError(t, err)
	authContext, err := run.deps.authLoader(run.fileSvc, run.cfg)
	require.NoError(t, err)
	_, err = evalBindSessions(cmd, run.cfg, run.fileSvc, defaultEvalBindClient(run.cfg), authContext, []string{data.OperatorSessionID})
	require.NoError(t, err)

	previous := gwremote.PublicMirrorBootstrapURL
	gwremote.PublicMirrorBootstrapURL = run.stack.Gateway.PublicMirrorBootstrapURL
	t.Cleanup(func() { gwremote.PublicMirrorBootstrapURL = previous })
	// Every projection of a test run must fit in the bootstrap window the
	// assertions read.
	run.stack.Gateway.Service.GetPublicMirror().SetMaxBootstrapProjections(1000)

	env := &runEnv{root: run.root, deps: run.deps, cmd: cmd, control: control,
		dataOperatorID: data.OperatorID, dataSession: data.OperatorSessionID, inferenceSession: inference.OperatorSessionID}
	return env, run
}

// readyReplies answers n model turns with READY.
func readyReplies(n int) []fixtures.OllamaReply {
	replies := make([]fixtures.OllamaReply, n)
	for i := range replies {
		replies[i] = fixtures.OllamaReply{Content: "READY"}
	}
	return replies
}

// createPublicationCampaign freezes a campaign over the one-scenario suite the
// scripted provider answers, repeated repetitions times.
func createPublicationCampaign(t *testing.T, run *realStackRun, campaignID string, repetitions uint32) {
	t.Helper()
	governancePosture, err := cases.WirePosture(constants.PostureDoctrine)
	require.NoError(t, err)
	dataWorkload, err := run.stack.Data.Workload()
	require.NoError(t, err)
	suite := evaluation.DefaultScenarioSuite()
	suite.ID = campaignID + "-suite"
	suite.Version = evaluation.SuiteSchemaVersion
	for _, scenario := range evaluation.DefaultScenarioSuite().Scenarios {
		if scenario.ScenarioID == "instruction-exact-format" {
			suite.Scenarios = []evaluation.SuiteScenario{scenario}
			break
		}
	}
	require.Len(t, suite.Scenarios, 1)
	require.NoError(t, evaluation.NewStore(run.fileSvc).CreateSuite(run.ctx, suite))
	_, err = createCampaign(run.ctx, run.deps, run.fileSvc, campaignCreateSpec{
		GovernancePosture: governancePosture,
		Platform:          testPlatform,
		CampaignID:        campaignID,
		Variants:          []*evalv1.ModelVariant{testQwenVariant()},
		Repetitions:       repetitions,
		SuiteID:           suite.ID,
		Investigation: &evalv1.EvaluationInvestigation{
			Mode:            evalv1.EvaluationInvestigationMode_EVALUATION_INVESTIGATION_MODE_PERSISTENT,
			InvestigationId: campaignID + "-investigation",
			DataOperatorId:  dataWorkload.OperatorID,
		},
	})
	require.NoError(t, err)
}

// onlyRunID returns the one run the test started.
func onlyRunID(t *testing.T, run *realStackRun) string {
	t.Helper()
	ids, err := evaluation.NewStore(run.fileSvc).ListRunIDs(run.ctx)
	require.NoError(t, err)
	require.Len(t, ids, 1)
	return ids[0]
}

func runAssignments(t *testing.T, run *realStackRun, runID string) []*evalv1.EvaluationAssignment {
	t.Helper()
	assignments, err := evaluation.NewStore(run.fileSvc).ListAssignments(run.ctx, runID)
	require.NoError(t, err)
	return assignments
}

// executeCampaign starts the campaign and executes every assignment without
// publishing, so publication is driven by the publish commands under test.
func executeCampaign(t *testing.T, env *runEnv, campaignID string) string {
	t.Helper()
	out, err := env.run(t, "runs", "start", campaignID, "--publish=false", "--no-backup")
	require.NoError(t, err, out)
	return out
}

// mirrorProjections reads the projections the real public mirror serves, oldest
// first, through the production bootstrap reader.
func mirrorProjections(t *testing.T) []publicationProjection {
	t.Helper()
	bootstrap, err := gwremote.FetchPublicMirrorBootstrap(t.Context())
	require.NoError(t, err)
	projections := make([]publicationProjection, 0, len(bootstrap.RecentProjections))
	for _, item := range bootstrap.RecentProjections {
		raw, err := json.Marshal(item)
		require.NoError(t, err)
		projection := publicationProjection{raw: string(raw), record: raw}
		if inner, ok := item["record"]; ok {
			projection.record = inner
		}
		projection.key, _ = item.StringField("idempotency_key")
		if seq, ok := item["sequence"]; ok {
			require.NoError(t, json.Unmarshal(seq, &projection.sequence))
		}
		projections = append(projections, projection)
	}
	sort.SliceStable(projections, func(i, j int) bool { return projections[i].sequence < projections[j].sequence })
	return projections
}

func mirrorHighWater(t *testing.T) int64 {
	t.Helper()
	bootstrap, err := gwremote.FetchPublicMirrorBootstrap(t.Context())
	require.NoError(t, err)
	return bootstrap.Snapshot.HighWaterSequence
}

func projectionsWithKey(projections []publicationProjection, key string) []publicationProjection {
	var found []publicationProjection
	for _, projection := range projections {
		if projection.key == key {
			found = append(found, projection)
		}
	}
	return found
}

// mirrorSummaries returns the evaluation_summary revisions the mirror serves,
// oldest first.
func mirrorSummaries(t *testing.T) []publicationSummary {
	t.Helper()
	var summaries []publicationSummary
	for _, projection := range mirrorProjections(t) {
		var summary publicationSummary
		require.NoError(t, json.Unmarshal(projection.record, &summary))
		if summary.Kind != "evaluation_summary" {
			continue
		}
		summary.sequence = projection.sequence
		summaries = append(summaries, summary)
	}
	return summaries
}

func latestSummary(t *testing.T) publicationSummary {
	t.Helper()
	summaries := mirrorSummaries(t)
	require.NotEmpty(t, summaries, "the mirror serves an evaluation summary")
	return summaries[len(summaries)-1]
}

func publishedCount(t *testing.T, out string) int {
	t.Helper()
	match := publishedCountPattern.FindStringSubmatch(out)
	require.Len(t, match, 2, out)
	var count int
	_, err := fmt.Sscanf(match[1], "%d", &count)
	require.NoError(t, err)
	return count
}

// assertOnceEach requires every key to be served by the mirror exactly once.
func assertOnceEach(t *testing.T, projections []publicationProjection, keys []string, message string) {
	t.Helper()
	for _, key := range keys {
		assert.Len(t, projectionsWithKey(projections, key), 1, "%s: %s", message, key)
	}
}

func resultKeys(runID string, assignments []*evalv1.EvaluationAssignment) []string {
	keys := make([]string, 0, len(assignments))
	for _, assignment := range assignments {
		keys = append(keys, evaluation.AssignmentResultIdempotencyKey(runID, assignment.GetAssignmentId()))
	}
	return keys
}

// digestsIn returns the distinct SHA-256 hex strings a published record cites.
func digestsIn(raw []byte) []string {
	seen := map[string]bool{}
	var digests []string
	for _, digest := range sha256HexPattern.FindAllString(string(raw), -1) {
		if !seen[digest] {
			seen[digest] = true
			digests = append(digests, digest)
		}
	}
	sort.Strings(digests)
	return digests
}

// editPublicationState loads the run's host publication state through the
// production store, applies edit, and saves it back.
func editPublicationState(t *testing.T, run *realStackRun, runID string, edit func(*evaluation.CampaignPublicationState)) {
	t.Helper()
	store, err := gwremote.NewCampaignPublicationStateStore(run.fileSvc, run.cfg)
	require.NoError(t, err)
	state, err := store.Load(run.ctx, runID)
	require.NoError(t, err)
	edit(state)
	require.NoError(t, store.Save(run.ctx, state))
}

func loadPublicationState(t *testing.T, run *realStackRun, runID string) *evaluation.CampaignPublicationState {
	t.Helper()
	store, err := gwremote.NewCampaignPublicationStateStore(run.fileSvc, run.cfg)
	require.NoError(t, err)
	state, err := store.Load(run.ctx, runID)
	require.NoError(t, err)
	return state
}

func TestPublicationRealStack_ScheduledLifecyclesAreExportedOnce(t *testing.T) {
	env, run := newPublicationEnv(t, readyReplies(3))
	createPublicationCampaign(t, run, "pub-scheduled", 3)
	out, err := env.run(t, "runs", "start", "pub-scheduled", "--prepare-only", "--no-backup")
	require.NoError(t, err, out)
	runID := onlyRunID(t, run)
	assignments := runAssignments(t, run, runID)
	require.Len(t, assignments, 3)

	queued := make([]string, 0, len(assignments))
	for _, assignment := range assignments {
		queued = append(queued, evaluation.AssignmentLifecycleIdempotencyKey(runID, assignment.GetAssignmentId(), evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_QUEUED))
	}
	assertOnceEach(t, mirrorProjections(t), queued, "scheduling publishes each queued lifecycle once")

	out, err = env.run(t, "runs", "publish", runID)
	require.NoError(t, err, out)
	settled := mirrorHighWater(t)
	out, err = env.run(t, "runs", "publish", runID)
	require.NoError(t, err, out)
	assert.Zero(t, publishedCount(t, out), "a republish of an already-published run exports nothing")
	assert.Equal(t, settled, mirrorHighWater(t), "the mirror high-water does not move on an idempotent publish")
	assertOnceEach(t, mirrorProjections(t), queued, "idempotent publish keeps each lifecycle single")
}

func TestPublicationRealStack_CatchUpExportsEveryCompletedAssignment(t *testing.T) {
	env, run := newPublicationEnv(t, readyReplies(3))
	createPublicationCampaign(t, run, "pub-catchup", 3)
	executeCampaign(t, env, "pub-catchup")
	runID := onlyRunID(t, run)
	assignments := runAssignments(t, run, runID)
	require.Len(t, assignments, 3)

	out, err := env.run(t, "runs", "publish", runID)
	require.NoError(t, err, out)
	assert.Positive(t, publishedCount(t, out), "catch-up exports the results that were never published")

	projections := mirrorProjections(t)
	assertOnceEach(t, projections, resultKeys(runID, assignments), "catch-up exports each result once")
	// The feed sequence is contiguous per served projection: no batch was
	// reordered or dropped on the way to the mirror.
	for index := 1; index < len(projections); index++ {
		assert.Greater(t, projections[index].sequence, projections[index-1].sequence)
	}
	assert.GreaterOrEqual(t, mirrorHighWater(t), int64(len(projections)))
}

func TestPublicationRealStack_CompletionIsNotRepublishedOnRepeat(t *testing.T) {
	env, run := newPublicationEnv(t, readyReplies(3))
	createPublicationCampaign(t, run, "pub-completion", 3)
	executeCampaign(t, env, "pub-completion")
	runID := onlyRunID(t, run)

	out, err := env.run(t, "runs", "verify", runID)
	require.NoError(t, err, out)
	assert.Contains(t, out, "PASS")
	completed := latestSummary(t)
	assert.NotEmpty(t, completed.RunStatus)
	settled := mirrorHighWater(t)

	_, err = env.run(t, "runs", "verify", runID)
	require.NoError(t, err)
	assert.Equal(t, settled, mirrorHighWater(t), "a repeated completion and verification exports nothing new")
}

func TestPublicationRealStack_ForceRepublishAppendsEveryProjectionAgain(t *testing.T) {
	env, run := newPublicationEnv(t, readyReplies(3))
	createPublicationCampaign(t, run, "pub-force", 3)
	executeCampaign(t, env, "pub-force")
	runID := onlyRunID(t, run)
	out, err := env.run(t, "runs", "publish", runID)
	require.NoError(t, err, out)
	caughtUp := mirrorHighWater(t)

	out, err = env.run(t, "runs", "publish", runID)
	require.NoError(t, err, out)
	assert.Equal(t, caughtUp, mirrorHighWater(t), "catch-up with nothing missing exports nothing")

	out, err = env.run(t, "runs", "publish", runID, "--force")
	require.NoError(t, err, out)
	assert.Contains(t, out, "forced republish")
	assert.Greater(t, mirrorHighWater(t), caughtUp, "a forced republish appends the projections again")
	for _, key := range resultKeys(runID, runAssignments(t, run, runID)) {
		assert.Len(t, projectionsWithKey(mirrorProjections(t), key), 2, "the forced republish serves each result a second time: %s", key)
	}
}

func TestPublicationRealStack_VerificationRevisionPublishesSummariesAndTranscript(t *testing.T) {
	env, run := newPublicationEnv(t, readyReplies(3))
	createPublicationCampaign(t, run, "pub-verify", 3)
	executeCampaign(t, env, "pub-verify")
	runID := onlyRunID(t, run)

	out, err := env.run(t, "runs", "verify", runID)
	require.NoError(t, err, out)
	assert.Contains(t, out, "PASS")
	report, err := evaluation.NewStore(run.fileSvc).LoadCampaignVerification(run.ctx, runID)
	require.NoError(t, err)

	summary := latestSummary(t)
	assert.Equal(t, "passed", summary.VerifierState)
	require.NotNil(t, summary.Metadata)
	assert.Equal(t, "bound", summary.Metadata.Provenance)
	assert.Equal(t, report.GetReportDigestRef().GetSha256(), summary.Metadata.ReportDigest)
	assert.Equal(t, report.GetVerifiedPopulationDigest(), summary.Metadata.PopulationDigest)

	models := 0
	for _, projection := range mirrorProjections(t) {
		var payload struct {
			Kind         string `json:"kind"`
			QualityState string `json:"quality_state"`
		}
		require.NoError(t, json.Unmarshal(projection.record, &payload))
		if payload.Kind == "model_summary" {
			models++
			assert.Equal(t, "exploratory_verified", payload.QualityState)
		}
	}
	assert.Positive(t, models, "verification publishes model summaries")

	projections := mirrorProjections(t)
	for _, assignment := range runAssignments(t, run, runID) {
		store := evaluation.NewStore(run.fileSvc)
		trace, err := store.LoadAssignmentAgentTrace(run.ctx, runID, assignment.GetAssignmentId())
		require.NoError(t, err)
		verified := projectionsWithKey(projections, evaluation.AssignmentVerifiedResultIdempotencyKey(runID, assignment.GetAssignmentId()))
		require.Len(t, verified, 1)
		assert.Contains(t, verified[0].raw, trace.FinalOutput, "the verified revision retains the saved transcript")
	}
}

func TestPublicationRealStack_BoundVerificationSurvivesForcedCatchUp(t *testing.T) {
	env, run := newPublicationEnv(t, readyReplies(3))
	createPublicationCampaign(t, run, "pub-survive", 3)
	executeCampaign(t, env, "pub-survive")
	runID := onlyRunID(t, run)
	out, err := env.run(t, "runs", "verify", runID)
	require.NoError(t, err, out)
	report, err := evaluation.NewStore(run.fileSvc).LoadCampaignVerification(run.ctx, runID)
	require.NoError(t, err)

	out, err = env.run(t, "runs", "publish", runID, "--force")
	require.NoError(t, err, out)

	latest := latestSummary(t)
	assert.Equal(t, "passed", latest.VerifierState, "later aggregate revisions keep the bound result")
	require.NotNil(t, latest.Metadata)
	assert.Equal(t, "bound", latest.Metadata.Provenance)
	assert.Equal(t, report.GetReportDigestRef().GetSha256(), latest.Metadata.ReportDigest)
	assert.Equal(t, report.GetVerifiedPopulationDigest(), latest.Metadata.PopulationDigest)
}

func TestPublicationRealStack_StalePartialVerificationDoesNotBindAnInProgressRun(t *testing.T) {
	env, run := newPublicationEnv(t, readyReplies(3))
	createPublicationCampaign(t, run, "pub-stale", 3)
	out, err := env.run(t, "runs", "start", "pub-stale", "--prepare-only", "--no-backup")
	require.NoError(t, err, out)
	runID := onlyRunID(t, run)

	out, err = env.run(t, "runs", "resume", runID, "--limit", "1", "--publish=false")
	require.NoError(t, err, out)
	// The partial verification binds only the one settled assignment.
	out, err = env.run(t, "runs", "verify", runID)
	require.NoError(t, err, out)
	require.Equal(t, "passed", latestSummary(t).VerifierState)

	out, err = env.run(t, "runs", "resume", runID, "--limit", "1", "--publish=false")
	require.NoError(t, err, out)
	out, err = env.run(t, "runs", "publish", runID)
	require.NoError(t, err, out)

	latest := latestSummary(t)
	assert.Equal(t, "not_run", latest.VerifierState, "a stale partial verification is not applied to the grown population")
	assert.Nil(t, latest.Metadata)
}

func TestPublicationRealStack_MismatchedPersistedReportFailsClosedWithoutPublishing(t *testing.T) {
	env, run := newPublicationEnv(t, readyReplies(3))
	createPublicationCampaign(t, run, "pub-mismatch", 3)
	executeCampaign(t, env, "pub-mismatch")
	runID := onlyRunID(t, run)
	out, err := env.run(t, "runs", "verify", runID)
	require.NoError(t, err, out)
	before := latestSummary(t)
	settled := mirrorHighWater(t)

	store := evaluation.NewStore(run.fileSvc)
	report, err := store.LoadCampaignVerification(run.ctx, runID)
	require.NoError(t, err)
	report.VerifiedPopulationDigest = strings.Repeat("9", 64)
	require.NoError(t, store.SaveCampaignVerification(run.ctx, runID, report))

	_, err = env.run(t, "runs", "publish", runID)
	require.ErrorIs(t, err, constants.ErrEvidenceScopeMismatch)
	assert.Equal(t, settled, mirrorHighWater(t), "a mismatched report publishes nothing")
	assert.Equal(t, before, latestSummary(t), "the mirror keeps the last applicable summary")
}

func TestPublicationRealStack_BoundFailureIsProjected(t *testing.T) {
	env, run := newPublicationEnv(t, readyReplies(3))
	createPublicationCampaign(t, run, "pub-failure", 3)
	executeCampaign(t, env, "pub-failure")
	runID := onlyRunID(t, run)
	out, err := env.run(t, "runs", "verify", runID)
	require.NoError(t, err, out)

	store := evaluation.NewStore(run.fileSvc)
	report, err := store.LoadCampaignVerification(run.ctx, runID)
	require.NoError(t, err)
	report.Status = evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL
	require.NoError(t, store.SaveCampaignVerification(run.ctx, runID, report))

	out, err = env.run(t, "runs", "publish", runID)
	require.NoError(t, err, out)
	latest := latestSummary(t)
	assert.Equal(t, "failed", latest.VerifierState)
	require.NotNil(t, latest.Metadata)
	assert.Equal(t, "bound", latest.Metadata.Provenance)
	assert.Equal(t, "failed", latest.Metadata.VerifierState)
	assert.Equal(t, report.GetReportDigestRef().GetSha256(), latest.Metadata.ReportDigest)
}

func TestPublicationRealStack_FailedAssignmentWithoutInferencesIsPublished(t *testing.T) {
	env, run := newPublicationEnv(t, []fixtures.OllamaReply{{Failure: "provider unavailable"}})
	createPublicationCampaign(t, run, "pub-failed", 1)
	// A failed assignment is the subject of this test, so the start command's
	// outcome is not the assertion; the persisted result is.
	_, _ = env.run(t, "runs", "start", "pub-failed", "--publish=false", "--no-backup")
	runID := onlyRunID(t, run)
	assignments := runAssignments(t, run, runID)
	require.Len(t, assignments, 1)
	assignmentID := assignments[0].GetAssignmentId()

	result, err := evaluation.NewStore(run.fileSvc).LoadAssignmentResult(run.ctx, runID, assignmentID)
	require.NoError(t, err)
	assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_FAILED, result.GetLifecycleStatus())
	assert.Empty(t, result.GetModelInferences())

	out, err := env.run(t, "runs", "publish", runID)
	require.NoError(t, err, out)
	assertOnceEach(t, mirrorProjections(t), []string{evaluation.AssignmentResultIdempotencyKey(runID, assignmentID)}, "the failed assignment's result is exported once")
}

func TestPublicationRealStack_AssignmentResultBindsSealedObservationWindow(t *testing.T) {
	env, run := newPublicationEnv(t, readyReplies(1))
	createPublicationCampaign(t, run, "pub-observed", 1)
	executeCampaign(t, env, "pub-observed")
	runID := onlyRunID(t, run)
	assignments := runAssignments(t, run, runID)
	require.Len(t, assignments, 1)
	trace, err := evaluation.NewStore(run.fileSvc).LoadAssignmentAgentTrace(run.ctx, runID, assignments[0].GetAssignmentId())
	require.NoError(t, err)
	require.Len(t, trace.ModelCalls, 1)

	out, err := env.run(t, "runs", "publish", runID)
	require.NoError(t, err, out)
	projections := projectionsWithKey(mirrorProjections(t), evaluation.AssignmentResultIdempotencyKey(runID, assignments[0].GetAssignmentId()))
	require.Len(t, projections, 1)
	t.Logf("assignment result record: %s", string(projections[0].record))
	assert.Contains(t, string(projections[0].record), "benchmark_observations")
}

func TestPublicationRealStack_CatchUpSkipsProofRebuildWhenHashesAreRecorded(t *testing.T) {
	env, run := newPublicationEnv(t, readyReplies(3))
	createPublicationCampaign(t, run, "pub-proof-skip", 3)
	executeCampaign(t, env, "pub-proof-skip")
	runID := onlyRunID(t, run)
	out, err := env.run(t, "runs", "publish", runID)
	require.NoError(t, err, out)

	bootstrap, err := gwremote.FetchPublicMirrorBootstrap(t.Context())
	require.NoError(t, err)
	require.Positive(t, bootstrap.ProofCatalogSummary.ArtifactCount, "the first publish ingests the audit proofs")
	require.NotNil(t, bootstrap.ProofCatalogSummary.LastGeneratedAt)
	ingestedAt := *bootstrap.ProofCatalogSummary.LastGeneratedAt

	out, err = env.run(t, "runs", "publish", runID)
	require.NoError(t, err, out)
	bootstrap, err = gwremote.FetchPublicMirrorBootstrap(t.Context())
	require.NoError(t, err)
	require.NotNil(t, bootstrap.ProofCatalogSummary.LastGeneratedAt)
	assert.True(t, ingestedAt.Equal(*bootstrap.ProofCatalogSummary.LastGeneratedAt), "a catch-up with every hash recorded does not rebuild the proof catalog")
}

func TestPublicationRealStack_CatchUpRebuildsProofWhenTraceOrFixtureIsUnpublished(t *testing.T) {
	tests := []struct {
		name   string
		forget func(*evaluation.CampaignPublishedProofArtifacts)
		check  func(*testing.T, evaluation.CampaignPublishedProofArtifacts)
	}{
		{
			name:   "agent trace unpublished",
			forget: func(artifacts *evaluation.CampaignPublishedProofArtifacts) { artifacts.AgentTraceSHA256 = "" },
			check: func(t *testing.T, artifacts evaluation.CampaignPublishedProofArtifacts) {
				assert.NotEmpty(t, artifacts.AgentTraceSHA256, "the rebuild binds the trace again")
			},
		},
		{
			name: "scenario fixture unpublished",
			forget: func(artifacts *evaluation.CampaignPublishedProofArtifacts) {
				artifacts.ScenarioInputSHA256 = ""
				artifacts.ScenarioGoldSHA256 = ""
			},
			check: func(t *testing.T, artifacts evaluation.CampaignPublishedProofArtifacts) {
				assert.NotEmpty(t, artifacts.ScenarioInputSHA256, "the rebuild binds the scenario input again")
				assert.NotEmpty(t, artifacts.ScenarioGoldSHA256, "the rebuild binds the scenario gold again")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, run := newPublicationEnv(t, readyReplies(3))
			createPublicationCampaign(t, run, "pub-proof-rebuild", 3)
			executeCampaign(t, env, "pub-proof-rebuild")
			runID := onlyRunID(t, run)
			out, err := env.run(t, "runs", "publish", runID)
			require.NoError(t, err, out)
			bootstrap, err := gwremote.FetchPublicMirrorBootstrap(t.Context())
			require.NoError(t, err)
			require.NotNil(t, bootstrap.ProofCatalogSummary.LastGeneratedAt)
			before := *bootstrap.ProofCatalogSummary.LastGeneratedAt

			state := loadPublicationState(t, run, runID)
			assignmentID := runAssignments(t, run, runID)[0].GetAssignmentId()
			require.NotEmpty(t, state.PublishedProofArtifacts[assignmentID].DatabaseSHA256)
			editPublicationState(t, run, runID, func(state *evaluation.CampaignPublicationState) {
				artifacts := state.PublishedProofArtifacts[assignmentID]
				tt.forget(&artifacts)
				state.PublishedProofArtifacts[assignmentID] = artifacts
			})

			out, err = env.run(t, "runs", "publish", runID)
			require.NoError(t, err, out)
			bootstrap, err = gwremote.FetchPublicMirrorBootstrap(t.Context())
			require.NoError(t, err)
			require.NotNil(t, bootstrap.ProofCatalogSummary.LastGeneratedAt)
			assert.False(t, before.Equal(*bootstrap.ProofCatalogSummary.LastGeneratedAt), "the proof catalog is rebuilt and republished")
			tt.check(t, loadPublicationState(t, run, runID).PublishedProofArtifacts[assignmentID])
		})
	}
}

func TestPublicationRealStack_ProofIsIngestedBeforeResultsAndTheMirrorServesIt(t *testing.T) {
	env, run := newPublicationEnv(t, readyReplies(3))
	createPublicationCampaign(t, run, "pub-proof-order", 3)
	executeCampaign(t, env, "pub-proof-order")
	runID := onlyRunID(t, run)
	assignments := runAssignments(t, run, runID)

	out, err := env.run(t, "runs", "publish", runID)
	require.NoError(t, err, out)
	// The mirror push is deferred during catch-up and flushed before the command
	// returns, so the proof catalog is served once publish returns.
	bootstrap, err := gwremote.FetchPublicMirrorBootstrap(t.Context())
	require.NoError(t, err)
	assert.Positive(t, bootstrap.ProofCatalogSummary.ArtifactCount, "the flushed proof catalog is served by the mirror")

	projections := mirrorProjections(t)
	for _, assignment := range assignments {
		results := projectionsWithKey(projections, evaluation.AssignmentResultIdempotencyKey(runID, assignment.GetAssignmentId()))
		require.Len(t, results, 1)
		// A result record cites the Gateway's audit database digest, so the
		// proof was ingested before the record was exported.
		assert.NotEmpty(t, digestsIn(results[0].record), "the result record binds the gateway digest")
	}
}

func TestPublicationRealStack_VerifiedRepublishBindsTheSameGatewayDigest(t *testing.T) {
	env, run := newPublicationEnv(t, readyReplies(3))
	createPublicationCampaign(t, run, "pub-verified-proof", 3)
	executeCampaign(t, env, "pub-verified-proof")
	runID := onlyRunID(t, run)
	assignments := runAssignments(t, run, runID)
	out, err := env.run(t, "runs", "publish", runID)
	require.NoError(t, err, out)
	published := map[string][]string{}
	for _, assignment := range assignments {
		results := projectionsWithKey(mirrorProjections(t), evaluation.AssignmentResultIdempotencyKey(runID, assignment.GetAssignmentId()))
		require.Len(t, results, 1)
		published[assignment.GetAssignmentId()] = digestsIn(results[0].record)
	}

	out, err = env.run(t, "runs", "verify", runID)
	require.NoError(t, err, out)
	for _, assignment := range assignments {
		verified := projectionsWithKey(mirrorProjections(t), evaluation.AssignmentVerifiedResultIdempotencyKey(runID, assignment.GetAssignmentId()))
		require.Len(t, verified, 1)
		// The Gateway reuses an assignment's database by digest, so the
		// verified record cites the digest the catch-up published.
		assert.Equal(t, published[assignment.GetAssignmentId()], digestsIn(verified[0].record))
	}
}

func TestPublicationRealStack_RunAggregatesFollowTheRunLeaseStatus(t *testing.T) {
	env, run := newPublicationEnv(t, readyReplies(3))
	createPublicationCampaign(t, run, "pub-lease", 3)
	out, err := env.run(t, "runs", "start", "pub-lease", "--prepare-only", "--no-backup")
	require.NoError(t, err, out)
	runID := onlyRunID(t, run)
	out, err = env.run(t, "runs", "publish", runID)
	require.NoError(t, err, out)
	assert.Equal(t, "scheduled", latestSummary(t).RunStatus)

	host, err := os.Hostname()
	require.NoError(t, err)
	store := evaluation.NewStore(run.fileSvc)
	lease := evaluation.RunLease{RunID: runID, PID: os.Getpid(), Host: host, StartedAt: time.Now().UTC()}
	_, err = store.AcquireRunLease(run.ctx, lease, nil)
	require.NoError(t, err)
	out, err = env.run(t, "runs", "publish", runID)
	require.NoError(t, err, out)
	assert.Equal(t, "running", latestSummary(t).RunStatus, "a live lease holder makes the run running without any progress change")

	require.NoError(t, store.ReleaseRunLease(run.ctx, runID, lease.PID))
	out, err = env.run(t, "runs", "publish", runID)
	require.NoError(t, err, out)
	assert.Equal(t, "scheduled", latestSummary(t).RunStatus, "releasing the lease publishes the scheduled status again")
}

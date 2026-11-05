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
	"net/http"
	"os"
	"path"
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
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	"github.com/zerotrustdata/ztd/test/fixtures"
	"google.golang.org/protobuf/proto"
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
	return newPublicationEnvWithOptions(t, fixtures.EvalStackOptions{Posture: constants.PostureDoctrine, Replies: replies, EnablePublicSpectator: true})
}

func newPublicationEnvWithOptions(t *testing.T, options fixtures.EvalStackOptions) (*runEnv, *realStackRun) {
	t.Helper()
	control := newFakeProcessControl()
	run := newRealStackEnvironment(t, options, testRunControl(control))
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

	previous := gwremote.PublicMirrorOrigin
	gwremote.PublicMirrorOrigin = run.stack.Gateway.PublicMirrorOrigin
	t.Cleanup(func() { gwremote.PublicMirrorOrigin = previous })
	env := &runEnv{root: run.root, deps: run.deps, cmd: cmd, control: control,
		dataOperatorID: data.OperatorID, dataSession: data.OperatorSessionID, inferenceSession: inference.OperatorSessionID}
	return env, run
}

// severityReplies answers n model turns with ERROR.
func severityReplies(n int) []fixtures.OllamaReply {
	replies := make([]fixtures.OllamaReply, n)
	for i := range replies {
		replies[i] = fixtures.OllamaReply{Content: "ERROR"}
	}
	return replies
}

// createPublicationCampaign freezes a campaign over the one-scenario suite the
// scripted provider answers, repeated repetitions times.
func createPublicationCampaign(t *testing.T, run *realStackRun, campaignID string, repetitions uint32) {
	t.Helper()
	createPublicationCampaignOn(t, run, campaignID, repetitions, testPlatform)
}

// createPublicationCampaignOn is createPublicationCampaign frozen under the
// given platform identity.
func createPublicationCampaignOn(t *testing.T, run *realStackRun, campaignID string, repetitions uint32, platform evaluation.PlatformIdentity) {
	t.Helper()
	dataWorkload, err := run.stack.Data.Workload()
	require.NoError(t, err)
	suite := evaluation.DefaultScenarioSuite()
	suite.ID = campaignID + "-suite"
	suite.Version = evaluation.SuiteSchemaVersion
	for _, scenario := range evaluation.DefaultScenarioSuite().Scenarios {
		if scenario.ScenarioID == "instruction-classify-severity" {
			// The catalog scenario is eligible for primary and assistant; the fixture executes
			// one turn per repetition, so restrict it to the primary role.
			scenario.EligibleRoles = []string{"primary"}
			suite.Scenarios = []evaluation.SuiteScenario{scenario}
			break
		}
	}
	require.Len(t, suite.Scenarios, 1)
	require.NoError(t, evaluation.NewStore(run.fileSvc).CreateSuite(run.ctx, suite))
	_, err = createCampaign(run.ctx, run.deps, run.fileSvc, campaignCreateSpec{
		Platform:    platform,
		CampaignID:  campaignID,
		Variants:    []*evalv1.ModelVariant{testQwenVariant()},
		Repetitions: repetitions,
		SuiteID:     suite.ID,
		Investigation: &evalv1.EvaluationInvestigation{
			Mode:            evalv1.EvaluationInvestigationMode_EVALUATION_INVESTIGATION_MODE_PERSISTENT,
			InvestigationId: campaignID + "-investigation",
			DataOperatorId:  dataWorkload.OperatorID,
		},
		Judge: testJudgeVariant(),
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

// executeCampaign starts the campaign and executes every assignment with live
// publication, as production runs do. The Gateway builds each assignment's audit
// proof from the live event records it retains.
func executeCampaign(t *testing.T, env *runEnv, campaignID string) string {
	t.Helper()
	out, err := env.run(t, "runs", "start", campaignID)
	require.NoError(t, err, out)
	return out
}

// verifyCLI runs runs verify --publish and fails on any publication warning:
// verify reports a failed mirror publication as a warning and still succeeds,
// so a silent publication failure would otherwise look like a pass.
func verifyCLI(t *testing.T, env *runEnv, runID string) (string, error) {
	t.Helper()
	out, err := env.run(t, "runs", "verify", runID, "--publish")
	if err == nil {
		require.NotContains(t, out, "warning:", "verify published without warnings")
	}
	return out, err
}

// mirrorProjections reads signed publication history for append/idempotency
// assertions. Bootstrap serves current entities and contains no old revisions.
func mirrorProjections(t *testing.T) []publicationProjection {
	t.Helper()
	var items []models.PublicFeedObject
	var cursor string
	for {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, gwremote.PublicMirrorOrigin+"/history?limit=500", nil)
		require.NoError(t, err)
		query := request.URL.Query()
		query.Set("cursor", cursor)
		request.URL.RawQuery = query.Encode()
		response, err := http.DefaultClient.Do(request)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, response.StatusCode)
		var page models.PublicFeedCursorPage
		err = json.NewDecoder(response.Body).Decode(&page)
		require.NoError(t, response.Body.Close())
		require.NoError(t, err)
		items = append(items, page.Items...)
		if !page.HasMore {
			break
		}
		require.NotEqual(t, cursor, page.Cursor, "history must advance")
		cursor = page.Cursor
	}
	projections := make([]publicationProjection, 0, len(items))
	for _, item := range items {
		recordType, _ := item.StringField("record_type")
		if recordType != string(models.PublicFeedRecordTypeProjection) {
			continue
		}
		delete(item, "record_type")
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
	env, run := newPublicationEnv(t, severityReplies(3))
	createPublicationCampaign(t, run, "pub-scheduled", 3)
	out, err := env.run(t, "runs", "start", "pub-scheduled", "--prepare-only")
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
	before := mirrorProjections(t)
	out, err = env.run(t, "runs", "publish", runID)
	require.NoError(t, err, out)
	// A republish appends the live summary revision it reports and no lifecycle.
	assert.Len(t, mirrorProjections(t), len(before)+publishedCount(t, out), "the count printed matches the projections the mirror served")
	assertOnceEach(t, mirrorProjections(t), queued, "idempotent publish keeps each lifecycle single")
	assertOnceEach(t, mirrorProjections(t), queued, "idempotent publish keeps each lifecycle single")
}

func TestPublicationRealStack_CancelDoesNotPublishByDefaultAfterDeferredRun(t *testing.T) {
	env, run := newPublicationEnv(t, severityReplies(3))
	createPublicationCampaign(t, run, "pub-cancel-deferred", 3)
	out, err := env.run(t, "runs", "start", "pub-cancel-deferred", "--prepare-only", "--publish=false")
	require.NoError(t, err, out)
	runID := onlyRunID(t, run)
	before := mirrorProjections(t)

	out, err = env.run(t, "runs", "cancel", runID)
	require.NoError(t, err, out)
	assert.Equal(t, before, mirrorProjections(t), "cancel leaves the public mirror untouched unless --publish is supplied")
}

func TestPublicationRealStack_CatchUpAfterLiveExecutionKeepsEveryResultOnce(t *testing.T) {
	env, run := newPublicationEnv(t, severityReplies(3))
	createPublicationCampaign(t, run, "pub-catchup", 3)
	executeCampaign(t, env, "pub-catchup")
	runID := onlyRunID(t, run)
	assignments := runAssignments(t, run, runID)
	require.Len(t, assignments, 3)

	out, err := env.run(t, "runs", "publish", runID)
	require.NoError(t, err, out)
	t.Logf("catch-up after live execution published %d record(s)", publishedCount(t, out))

	projections := mirrorProjections(t)
	assertOnceEach(t, projections, resultKeys(runID, assignments), "catch-up leaves each result served once")
	// The feed sequence is contiguous per served projection: no batch was
	// reordered or dropped on the way to the mirror.
	for index := 1; index < len(projections); index++ {
		assert.Greater(t, projections[index].sequence, projections[index-1].sequence)
	}
	assert.GreaterOrEqual(t, mirrorHighWater(t), int64(len(projections)))
}

// Completion publishes the run's summary; a repeated verify re-exports summary
// revisions but never a second copy of a lifecycle or result record.
func TestPublicationRealStack_CompletionAndRepeatVerifyKeepEachResultSingle(t *testing.T) {
	env, run := newPublicationEnv(t, severityReplies(3))
	createPublicationCampaign(t, run, "pub-completion", 3)
	executeCampaign(t, env, "pub-completion")
	runID := onlyRunID(t, run)
	assignments := runAssignments(t, run, runID)

	out, err := verifyCLI(t, env, runID)
	require.NoError(t, err, out)
	assert.Contains(t, out, "PASS")
	assert.NotEmpty(t, latestSummary(t).RunStatus)
	assertOnceEach(t, mirrorProjections(t), resultKeys(runID, assignments), "completion leaves each result served once")

	_, err = verifyCLI(t, env, runID)
	require.NoError(t, err)
	assertOnceEach(t, mirrorProjections(t), resultKeys(runID, assignments), "a repeated verify does not duplicate a result")
	assert.Equal(t, "passed", latestSummary(t).VerifierState)
}

func TestPublicationRealStack_ForceRepublishAppendsEveryProjectionAgain(t *testing.T) {
	env, run := newPublicationEnv(t, severityReplies(3))
	createPublicationCampaign(t, run, "pub-force", 3)
	executeCampaign(t, env, "pub-force")
	runID := onlyRunID(t, run)
	out, err := env.run(t, "runs", "publish", runID)
	require.NoError(t, err, out)
	caughtUp := mirrorHighWater(t)

	// Each publish re-exports the run's live summary revision, so the high-water
	// moves; the result records themselves stay single.
	out, err = env.run(t, "runs", "publish", runID)
	require.NoError(t, err, out)
	assert.Greater(t, mirrorHighWater(t), caughtUp, "a publish re-exports the live summary revision")
	assertOnceEach(t, mirrorProjections(t), resultKeys(runID, runAssignments(t, run, runID)), "a plain publish keeps each result single")

	out, err = env.run(t, "runs", "publish", runID, "--force")
	require.NoError(t, err, out)
	assert.Contains(t, out, "forced republish")
	assert.Greater(t, mirrorHighWater(t), caughtUp, "a forced republish appends the projections again")
	for _, key := range resultKeys(runID, runAssignments(t, run, runID)) {
		assert.Len(t, projectionsWithKey(mirrorProjections(t), key), 2, "the forced republish serves each result a second time: %s", key)
	}
}

func TestPublicationRealStack_VerificationRevisionPublishesSummariesAndTranscript(t *testing.T) {
	env, run := newPublicationEnv(t, severityReplies(3))
	createPublicationCampaign(t, run, "pub-verify", 3)
	executeCampaign(t, env, "pub-verify")
	runID := onlyRunID(t, run)

	out, err := verifyCLI(t, env, runID)
	require.NoError(t, err, out)
	assert.Contains(t, out, "PASS")
	report, err := evaluation.NewStore(run.fileSvc).LoadCampaignVerification(run.ctx, runID)
	require.NoError(t, err)
	assertPopulationBoundReport(t, report, 3, 3)

	summary := latestSummary(t)
	assert.Equal(t, "passed", summary.VerifierState)
	require.NotNil(t, summary.Metadata)
	assert.Equal(t, "bound", summary.Metadata.Provenance)
	assert.Equal(t, report.GetReportDigestRef().GetSha256(), summary.Metadata.ReportDigest)
	assert.Equal(t, report.GetVerifiedPopulationDigest(), summary.Metadata.PopulationDigest)

	models := 0
	lastModelQuality := ""
	for _, projection := range mirrorProjections(t) {
		var payload struct {
			Kind         string `json:"kind"`
			QualityState string `json:"quality_state"`
		}
		require.NoError(t, json.Unmarshal(projection.record, &payload))
		if payload.Kind == "model_summary" {
			models++
			lastModelQuality = payload.QualityState
		}
	}
	assert.Positive(t, models, "verification publishes model summaries")
	assert.Equal(t, "exploratory_verified", lastModelQuality, "the verified revision is the latest model summary")

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
	env, run := newPublicationEnv(t, severityReplies(3))
	createPublicationCampaign(t, run, "pub-survive", 3)
	executeCampaign(t, env, "pub-survive")
	runID := onlyRunID(t, run)
	out, err := verifyCLI(t, env, runID)
	require.NoError(t, err, out)
	report, err := evaluation.NewStore(run.fileSvc).LoadCampaignVerification(run.ctx, runID)
	require.NoError(t, err)
	assertPopulationBoundReport(t, report, 3, 3)

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
	env, run := newPublicationEnv(t, severityReplies(3))
	createPublicationCampaign(t, run, "pub-stale", 3)
	out, err := env.run(t, "runs", "start", "pub-stale", "--prepare-only")
	require.NoError(t, err, out)
	runID := onlyRunID(t, run)

	out, err = env.run(t, "runs", "resume", runID, "--limit", "1", "--publish")
	require.NoError(t, err, out)
	// The partial verification binds only the one settled assignment.
	out, err = verifyCLI(t, env, runID)
	require.NoError(t, err, out)

	// Execution publishes the aggregate after the grown population settles; the
	// stale partial report must not block it. --verify=false keeps the report
	// stale, since a verifying resume would rebuild it over the grown population.
	out, err = env.run(t, "runs", "resume", runID, "--limit", "1", "--publish", "--verify=false")
	require.NoError(t, err, out)

	latest := latestSummary(t)
	assert.Equal(t, "not_run", latest.VerifierState, "a stale partial verification is not applied to the grown population")
	assert.Nil(t, latest.Metadata)
}

// runs publish of an unsettled run follows the aggregate path: a stale partial
// verification is not bound, so the publish continues instead of failing closed.
func TestPublicationRealStack_RunsPublishIgnoresStalePartialVerificationOnUnsettledRun(t *testing.T) {
	env, run := newPublicationEnv(t, severityReplies(3))
	createPublicationCampaign(t, run, "pub-stale-publish", 3)
	out, err := env.run(t, "runs", "start", "pub-stale-publish", "--prepare-only")
	require.NoError(t, err, out)
	runID := onlyRunID(t, run)

	out, err = env.run(t, "runs", "resume", runID, "--limit", "1", "--publish")
	require.NoError(t, err, out)
	out, err = verifyCLI(t, env, runID)
	require.NoError(t, err, out)
	// --verify=false keeps the partial report stale while the population grows.
	out, err = env.run(t, "runs", "resume", runID, "--limit", "1", "--publish", "--verify=false")
	require.NoError(t, err, out)

	out, err = env.run(t, "runs", "publish", runID)
	require.NoError(t, err, out)
	latest := latestSummary(t)
	assert.Equal(t, "not_run", latest.VerifierState, "a stale partial verification is not bound to the grown population")
	assert.Nil(t, latest.Metadata)
}

// A run executed with publication off never sent its live event records to the
// Gateway. The deferred publish must deliver them before it requests each
// assignment's audit proof, or the Gateway has no records to build the database
// from.
func TestPublicationRealStack_DeferredPublishDeliversEventsBeforeAuditProof(t *testing.T) {
	env, run := newPublicationEnv(t, severityReplies(3))
	createPublicationCampaign(t, run, "pub-deferred", 3)
	out, err := env.run(t, "runs", "start", "pub-deferred", "--publish=false")
	require.NoError(t, err, out)
	runID := onlyRunID(t, run)

	out, err = env.run(t, "runs", "publish", runID)
	require.NoError(t, err, out)

	bootstrap, err := gwremote.FetchPublicMirrorBootstrap(t.Context())
	require.NoError(t, err)
	assert.Positive(t, bootstrap.ProofCatalogSummary.ArtifactCount, "the deferred publish ingests the audit proofs")
	state := loadPublicationState(t, run, runID)
	for _, assignment := range runAssignments(t, run, runID) {
		assert.NotEmpty(t, state.PublishedProofArtifacts[assignment.GetAssignmentId()].DatabaseSHA256,
			"the gateway database digest is recorded for assignment %s", assignment.GetAssignmentId())
	}

	out, err = verifyCLI(t, env, runID)
	require.NoError(t, err, out)
}

func TestPublicationRealStack_MismatchedPersistedReportFailsClosedWithoutPublishing(t *testing.T) {
	env, run := newPublicationEnv(t, severityReplies(3))
	createPublicationCampaign(t, run, "pub-mismatch", 3)
	executeCampaign(t, env, "pub-mismatch")
	runID := onlyRunID(t, run)
	out, err := verifyCLI(t, env, runID)
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
	env, run := newPublicationEnv(t, severityReplies(3))
	createPublicationCampaign(t, run, "pub-failure", 3)
	executeCampaign(t, env, "pub-failure")
	runID := onlyRunID(t, run)
	out, err := verifyCLI(t, env, runID)
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

func TestPublicationRealStack_ProviderFailedAssignmentIsPublishedOnce(t *testing.T) {
	env, run := newPublicationEnv(t, []fixtures.OllamaReply{{Failure: "provider unavailable"}})
	createPublicationCampaign(t, run, "pub-failed", 1)
	// The failed turn is the subject of this test, so the start command's exit
	// status is not the assertion; the persisted result is.
	_, _ = env.run(t, "runs", "start", "pub-failed")
	runID := onlyRunID(t, run)
	assignments := runAssignments(t, run, runID)
	require.Len(t, assignments, 1)
	assignmentID := assignments[0].GetAssignmentId()

	result, err := evaluation.NewStore(run.fileSvc).LoadAssignmentResult(run.ctx, runID, assignmentID)
	require.NoError(t, err)
	// A ScriptedOllama Failure is recorded as PROVIDER_FAILED with the failed
	// turn as an inference, so the real path never yields a result without
	// inferences; that shape stays with the typed unit test.
	assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_PROVIDER_FAILED, result.GetLifecycleStatus())
	require.NotEmpty(t, result.GetModelInferences())

	out, err := env.run(t, "runs", "publish", runID)
	require.NoError(t, err, out)
	assertOnceEach(t, mirrorProjections(t), []string{evaluation.AssignmentResultIdempotencyKey(runID, assignmentID)}, "the provider-failed assignment's result is exported once")
}

func TestPublicationRealStack_AssignmentResultBindsSealedObservationWindow(t *testing.T) {
	env, run := newPublicationEnv(t, severityReplies(1))
	createPublicationCampaign(t, run, "pub-observed", 1)
	executeCampaign(t, env, "pub-observed")
	runID := onlyRunID(t, run)
	assignments := runAssignments(t, run, runID)
	require.Len(t, assignments, 1)
	trace, err := evaluation.NewStore(run.fileSvc).LoadAssignmentAgentTrace(run.ctx, runID, assignments[0].GetAssignmentId())
	require.NoError(t, err)
	require.Len(t, trace.ModelCalls, 1)

	attemptID := trace.ModelCalls[0].ProviderAttemptID
	require.NotEmpty(t, attemptID)
	window, err := evaluation.NewStore(run.fileSvc).LoadProviderObservationWindow(run.ctx, runID, attemptID)
	require.NoError(t, err, "the real Inference observer seals a window for the provider attempt")
	require.NotNil(t, window)
	require.NotEmpty(t, window.GetObservationDigest())

	out, err := env.run(t, "runs", "publish", runID)
	require.NoError(t, err, out)
	projections := projectionsWithKey(mirrorProjections(t), evaluation.AssignmentResultIdempotencyKey(runID, assignments[0].GetAssignmentId()))
	require.Len(t, projections, 1)
	// The public record carries the derived benchmark observations, not the
	// window digest, so the window itself is asserted on the local store above.
	assert.Contains(t, string(projections[0].record), "benchmark_observations", "the result publishes its sealed observation")
}

func TestPublicationRealStack_CatchUpSkipsProofRebuildWhenHashesAreRecorded(t *testing.T) {
	env, run := newPublicationEnv(t, severityReplies(3))
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
				t.Helper()
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
				t.Helper()
				assert.NotEmpty(t, artifacts.ScenarioInputSHA256, "the rebuild binds the scenario input again")
				assert.NotEmpty(t, artifacts.ScenarioGoldSHA256, "the rebuild binds the scenario gold again")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, run := newPublicationEnv(t, severityReplies(3))
			createPublicationCampaign(t, run, "pub-proof-rebuild", 3)
			executeCampaign(t, env, "pub-proof-rebuild")
			runID := onlyRunID(t, run)
			out, err := env.run(t, "runs", "publish", runID)
			require.NoError(t, err, out)
			bootstrap, err := gwremote.FetchPublicMirrorBootstrap(t.Context())
			require.NoError(t, err)
			require.NotNil(t, bootstrap.ProofCatalogSummary.LastGeneratedAt)
			before := *bootstrap.ProofCatalogSummary.LastGeneratedAt

			assignments := runAssignments(t, run, runID)
			assignmentID := assignments[0].GetAssignmentId()
			require.NotEmpty(t, loadPublicationState(t, run, runID).PublishedProofArtifacts[assignmentID].DatabaseSHA256)
			// Drop the feed idempotency keys and keep the proof keys, as a feed
			// reset does, so only the recorded hashes decide what is rebuilt.
			proofKeys := make(map[string]bool, len(assignments))
			for _, assignment := range assignments {
				proofKeys[evaluation.AssignmentAuditProofIdempotencyKey(runID, assignment.GetAssignmentId())] = true
			}
			editPublicationState(t, run, runID, func(state *evaluation.CampaignPublicationState) {
				kept := make([]string, 0, len(proofKeys))
				for _, key := range state.PublishedIdempotency {
					if proofKeys[key] {
						kept = append(kept, key)
					}
				}
				state.PublishedIdempotency = kept
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
	env, run := newPublicationEnv(t, severityReplies(3))
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
	env, run := newPublicationEnv(t, severityReplies(3))
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

	out, err = verifyCLI(t, env, runID)
	require.NoError(t, err, out)
	for _, assignment := range assignments {
		verified := projectionsWithKey(mirrorProjections(t), evaluation.AssignmentVerifiedResultIdempotencyKey(runID, assignment.GetAssignmentId()))
		require.Len(t, verified, 1)
		// The verified record still cites every digest the catch-up published for
		// the assignment's audit database; it also carries the verification's own
		// digests, so containment is the invariant.
		verifiedDigests := digestsIn(verified[0].record)
		for _, digest := range published[assignment.GetAssignmentId()] {
			assert.Contains(t, verifiedDigests, digest, "the verified record binds the gateway database digest")
		}
	}
}

func TestPublicationRealStack_RunAggregatesFollowTheRunLeaseStatus(t *testing.T) {
	env, run := newPublicationEnv(t, severityReplies(3))
	createPublicationCampaign(t, run, "pub-lease", 3)
	out, err := env.run(t, "runs", "start", "pub-lease", "--prepare-only")
	require.NoError(t, err, out)
	runID := onlyRunID(t, run)
	store := evaluation.NewStore(run.fileSvc)

	// Each publish appends exactly one aggregate revision, so a status change is
	// never deduplicated against an unchanged assignment count.
	assertStatus := func(want string) {
		t.Helper()
		before := len(mirrorSummaries(t))
		out, err := env.run(t, "runs", "publish", runID)
		require.NoError(t, err, out)
		summaries := mirrorSummaries(t)
		require.Len(t, summaries, before+1, "a publish appends one aggregate revision")
		assert.Equal(t, want, summaries[len(summaries)-1].RunStatus)
		bootstrap, err := gwremote.FetchPublicMirrorBootstrap(t.Context())
		require.NoError(t, err)
		currentCount := 0
		for _, item := range bootstrap.RecentProjections {
			kind, _ := item.StringField("kind")
			if kind != "evaluation_summary" {
				continue
			}
			currentCount++
			status, _ := item.StringField("run_status")
			assert.Equal(t, want, status, "current snapshot agrees with the latest signed revision")
		}
		require.Equal(t, 1, currentCount, "bootstrap contains only the current run summary")
	}
	assertStatus("scheduled")

	host, err := os.Hostname()
	require.NoError(t, err)
	lease := evaluation.RunLease{RunID: runID, PID: os.Getpid(), Host: host, StartedAt: time.Now().UTC()}
	_, err = store.AcquireRunLease(run.ctx, lease, nil)
	require.NoError(t, err)
	assertStatus("running")

	require.NoError(t, store.ReleaseRunLease(run.ctx, runID, lease.PID))
	assertStatus("scheduled")

	// An assignment left running with no live lease holder, as after a crash, is
	// interrupted.
	assignments := runAssignments(t, run, runID)
	require.NotEmpty(t, assignments)
	assignments[0].LifecycleStatus = evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_RUNNING
	require.NoError(t, store.SaveAssignment(run.ctx, assignments[0]))
	assertStatus("interrupted")

	// Cancel stops every assignment that has not settled, so the run reports cancelled.
	out, err = env.run(t, "runs", "cancel", runID, "--publish")
	require.NoError(t, err, out)
	assertStatus("cancelled")
}

// The backfill republishes the model-verification revisions whose idempotency
// keys were lost, and only those, and a second republish appends nothing.
func TestPublicationRealStack_VerificationBackfillRepublishesModelSummariesAfterKeysAreLost(t *testing.T) {
	env, run := newPublicationEnv(t, severityReplies(3))
	createPublicationCampaign(t, run, "pub-backfill", 3)
	// Execute without verifying, so the one verification below publishes the only
	// verified model revision the backfill must republish.
	out, err := env.run(t, "runs", "start", "pub-backfill", "--verify=false")
	require.NoError(t, err, out)
	runID := onlyRunID(t, run)
	out, err = verifyCLI(t, env, runID)
	require.NoError(t, err, out)
	report, err := evaluation.NewStore(run.fileSvc).LoadCampaignVerification(run.ctx, runID)
	require.NoError(t, err)
	assertPopulationBoundReport(t, report, 3, 3)
	verifiedBefore := verifiedModelSummaries(t)
	require.Positive(t, verifiedBefore, "verification publishes the verified model summaries")

	// Drop the model-verification keys and keep the run-level keys, as a lost
	// publication state would.
	editPublicationState(t, run, runID, func(state *evaluation.CampaignPublicationState) {
		kept := make([]string, 0, len(state.PublishedIdempotency))
		for _, key := range state.PublishedIdempotency {
			if !strings.Contains(key, ":verification:model:") {
				kept = append(kept, key)
			}
		}
		state.PublishedIdempotency = kept
	})

	publication, err := NewCampaignPublicationCoordinator(env.cmd, run.fileSvc, run.cfg)
	require.NoError(t, err)
	backfilled, err := publication.PublishRunVerification(run.ctx, runID, report)
	require.NoError(t, err)
	assert.Equal(t, verifiedBefore, backfilled, "the backfill republishes each verified model summary once")
	assert.Equal(t, verifiedBefore+backfilled, verifiedModelSummaries(t), "the mirror serves the backfilled revisions")

	retried, err := publication.PublishRunVerification(run.ctx, runID, report)
	require.NoError(t, err)
	assert.Zero(t, retried, "a second republish finds every key recorded")
	assert.Equal(t, verifiedBefore+backfilled, verifiedModelSummaries(t))
}

// Release provenance on the real stack: a campaign frozen with a recorded release,
// one frozen before releases were recorded and then asserted by an operator, and
// one with no release at all. Every projection the mirror serves carries the
// campaign's release, basis and source revision.
func TestPublicationRealStack_ReleaseProvenanceReachesEveryPublicRecord(t *testing.T) {
	recordedPlatform := evaluation.PlatformIdentity{Release: testReleaseVersion, SourceRevision: "3f9c2a1b0d4e5f60718293a4b5c6d7e8f9012345"}
	tests := []struct {
		name     string
		legacy   bool
		tagged   string
		platform evaluation.PlatformIdentity
		release  string
		basis    string
		revision string
	}{
		{name: "recorded", platform: recordedPlatform, release: testReleaseVersion, basis: "recorded", revision: recordedPlatform.SourceRevision},
		{name: "asserted", legacy: true, tagged: "v2.2.7", platform: testPlatform, release: "v2.2.7", basis: "asserted"},
		{name: "unknown", legacy: true, platform: testPlatform, basis: "unknown"},
	}
	// The bootstrap window serves projection records only. Live stage_updated and
	// metric_updated events are not in it, and their release identity is covered by
	// TestProjectLiveEvents_CarryTheCampaignReleaseIdentity.
	requiredKinds := []string{"PublicAssignmentLifecycleRecord", "PublicAssignmentResultProjection", "catalog_snapshot", "evaluation_summary", "model_summary", "methodology_snapshot"}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			campaignID := "pub-release-" + tt.name
			env, run := newPublicationEnv(t, severityReplies(3))
			createPublicationCampaignOn(t, run, campaignID, 3, tt.platform)
			if tt.legacy {
				markCampaignPreRelease(t, run, campaignID)
			}
			if tt.tagged != "" {
				out, err := env.run(t, "campaigns", "tag", "--release", tt.tagged, campaignID)
				require.NoError(t, err, out)
			}
			executeCampaign(t, env, campaignID)
			runID := onlyRunID(t, run)
			out, err := verifyCLI(t, env, runID)
			require.NoError(t, err, out)

			kinds := map[string]bool{}
			for _, projection := range mirrorProjections(t) {
				var envelope struct {
					Kind        string                      `json:"kind"`
					MessageType string                      `json:"message_type"`
					RecordType  models.PublicFeedRecordType `json:"record_type"`
				}
				require.NoError(t, json.Unmarshal([]byte(projection.raw), &envelope))
				// Live events keep their kind in the record body, not the envelope.
				var body struct {
					Kind string `json:"kind"`
				}
				require.NoError(t, json.Unmarshal(projection.record, &body))
				kind := envelope.Kind
				if kind == "" {
					kind = body.Kind
				}
				wantBasis := tt.basis
				// Protobuf-owned records and live events carry the enum name of the basis.
				if envelope.MessageType != "" {
					kind = envelope.MessageType
				}
				if envelope.MessageType != "" || envelope.RecordType == models.PublicFeedRecordTypeEvent {
					wantBasis = "PUBLIC_RELEASE_BASIS_" + strings.ToUpper(tt.basis)
				}
				var identity struct {
					Release        string `json:"release"`
					Basis          string `json:"release_basis"`
					SourceRevision string `json:"source_revision"`
				}
				require.NoError(t, json.Unmarshal(projection.record, &identity))
				assert.Equal(t, tt.release, identity.Release, kind)
				assert.Equal(t, wantBasis, identity.Basis, kind)
				assert.Equal(t, tt.revision, identity.SourceRevision, kind)
				kinds[kind] = true
			}
			for _, kind := range requiredKinds {
				assert.True(t, kinds[kind], "the mirror serves a public %s record", kind)
			}
		})
	}
}

// A release tag file that does not name its campaign fails publication closed:
// nothing reaches the mirror.
func TestPublicationRealStack_MalformedReleaseTagFailsPublishClosed(t *testing.T) {
	env, run := newPublicationEnv(t, severityReplies(3))
	createPublicationCampaignOn(t, run, "pub-bad-tag", 3, testPlatform)
	markCampaignPreRelease(t, run, "pub-bad-tag")
	out, err := env.run(t, "runs", "start", "pub-bad-tag", "--prepare-only")
	require.NoError(t, err, out)
	runID := onlyRunID(t, run)
	before := mirrorHighWater(t)

	tagPath := path.Join(constants.EvaluationCampaignsPath, "pub-bad-tag", constants.EvaluationCampaignReleaseTagFilename)
	require.NoError(t, run.fileSvc.WriteFile(run.ctx, tagPath, []byte(`{"campaign_id":"another","release":"v2.2.7"}`), constants.PermFilePrivate))

	out, err = env.run(t, "runs", "publish", runID)
	require.ErrorIs(t, err, constants.ErrEvidenceArtifactMalformed, out)
	assert.Equal(t, before, mirrorHighWater(t), "a malformed release tag publishes nothing")
}

// markCampaignPreRelease stores a frozen campaign the way a campaign frozen before
// releases were recorded is stored: no platform release or source revision, with
// the digest recomputed over the rest.
func markCampaignPreRelease(t *testing.T, run *realStackRun, campaignID string) {
	t.Helper()
	store := evaluation.NewStore(run.fileSvc)
	spec, err := store.LoadCampaignSpec(run.ctx, campaignID)
	require.NoError(t, err)
	spec.PlatformRelease, spec.SourceRevision = "", ""
	spec.CampaignDigest, err = evaluation.ComputeCampaignSpecDigest(spec)
	require.NoError(t, err)
	require.NoError(t, store.SaveCampaignSpec(run.ctx, spec))
}

// assertPopulationBoundReport checks a persisted verification report that binds
// the run's assignment population: expected assignments in the population, of
// which verified settled.
func assertPopulationBoundReport(t *testing.T, report *evalv1.EvaluationVerificationReport, expected, verified uint32) {
	t.Helper()
	require.NotNil(t, report)
	assert.Equal(t, constants.CampaignVerifierVersion, report.GetSchemaVersion())
	assert.Equal(t, constants.CampaignVerifierVersion, report.GetVerifierContractVersion())
	assert.Equal(t, constants.EvaluationSourceVersion, report.GetVerifierReleaseVersion())
	assert.Equal(t, expected, report.GetExpectedAssignmentCount())
	assert.Equal(t, verified, report.GetVerifiedAssignmentCount())
	assert.NotEmpty(t, report.GetVerifiedPopulationDigest())
	assert.NotEmpty(t, report.GetCampaignDigest())
	assert.NotEmpty(t, report.GetCatalogDigest())
	assert.NotEmpty(t, report.GetModelRegistryDigest())
	require.NotNil(t, report.GetReportDigestRef())
	assert.Len(t, report.GetReportDigestRef().GetSha256(), 64)
}

// verifiedModelSummaries counts the model summary revisions the mirror serves in
// a verified quality state.
func verifiedModelSummaries(t *testing.T) int {
	t.Helper()
	count := 0
	for _, projection := range mirrorProjections(t) {
		var payload struct {
			Kind         string `json:"kind"`
			QualityState string `json:"quality_state"`
		}
		require.NoError(t, json.Unmarshal(projection.record, &payload))
		if payload.Kind == "model_summary" && payload.QualityState == "exploratory_verified" {
			count++
		}
	}
	return count
}

// TestRunsVerifyPublish_FailureSupersedesEarlierPass covers the public downgrade:
// a stored result the verifier now rejects, with the population count unchanged,
// must replace an earlier published pass on the summary, every model summary, and
// every assignment row, and a later pass must restore all of them.
func TestRunsVerifyPublish_FailureSupersedesEarlierPass(t *testing.T) {
	env, run := newPublicationEnv(t, severityReplies(3))
	createPublicationCampaign(t, run, "pub-supersede", 3)
	executeCampaign(t, env, "pub-supersede")
	runID := onlyRunID(t, run)
	assignments := runAssignments(t, run, runID)

	out, err := verifyCLI(t, env, runID)
	require.NoError(t, err, out)
	assertPublishedOutcome(t, assignments, "passed", "verified", "exploratory_verified")

	store := evaluation.NewStore(run.fileSvc)
	results, err := store.LoadAssignmentResults(run.ctx, runID, assignments)
	require.NoError(t, err)
	require.NotEmpty(t, results)
	var original *evalv1.EvaluationAssignmentResult
	for _, result := range results {
		original = proto.Clone(result).(*evalv1.EvaluationAssignmentResult)
		result.LifecycleStatus = evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_INVALID_EVIDENCE
		result.ResultDigest, err = evaluation.ComputeAssignmentResultDigest(result)
		require.NoError(t, err)
		require.NoError(t, store.SaveAssignmentResult(run.ctx, result))
		break
	}

	out, err = verifyCLI(t, env, runID)
	require.ErrorIs(t, err, constants.ErrEvalRunVerificationFailed, out)
	assert.NotContains(t, out, "warning:", "the failing verification publishes without a warning")
	assertPublishedOutcome(t, assignments, "failed", "unverified", "")

	require.NoError(t, store.SaveAssignmentResult(run.ctx, original))
	out, err = verifyCLI(t, env, runID)
	require.NoError(t, err, out)
	assertPublishedOutcome(t, assignments, "passed", "verified", "exploratory_verified")

	_, err = verifyCLI(t, env, runID)
	require.NoError(t, err)
	projections := mirrorProjections(t)
	for _, assignment := range assignments {
		passKey := evaluation.AssignmentVerifiedResultIdempotencyKey(runID, assignment.GetAssignmentId())
		assert.Len(t, projectionsWithKey(projections, passKey), 2, "pass, fail, pass republishes once and a repeated pass adds nothing: %s", passKey)
		failKey := evaluation.AssignmentFailedVerificationResultIdempotencyKey(runID, assignment.GetAssignmentId())
		assert.Len(t, projectionsWithKey(projections, failKey), 1, "the failure publishes each row once: %s", failKey)
	}
}

// The stored report moves ahead of the feed whenever a plain verify stays local,
// so each drift row must be visible in the status the public status command prints.
func TestPublicStatus_ReportsFeedDriftForEachDisagreement(t *testing.T) {
	env, run := newPublicationEnv(t, severityReplies(3))
	createPublicationCampaign(t, run, "pub-drift", 3)
	out, err := env.run(t, "runs", "start", "pub-drift", "--verify=false")
	require.NoError(t, err, out)
	runID := onlyRunID(t, run)
	assignments := runAssignments(t, run, runID)
	store := evaluation.NewStore(run.fileSvc)
	results, err := store.LoadAssignmentResults(run.ctx, runID, assignments)
	require.NoError(t, err)
	require.NotEmpty(t, results)
	var original, rejected *evalv1.EvaluationAssignmentResult
	for _, result := range results {
		original = proto.Clone(result).(*evalv1.EvaluationAssignmentResult)
		rejected = proto.Clone(result).(*evalv1.EvaluationAssignmentResult)
		rejected.LifecycleStatus = evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_INVALID_EVIDENCE
		rejected.ResultDigest, err = evaluation.ComputeAssignmentResultDigest(rejected)
		require.NoError(t, err)
		break
	}
	localVerify := func(result *evalv1.EvaluationAssignmentResult) {
		t.Helper()
		require.NoError(t, store.SaveAssignmentResult(run.ctx, result))
		_, _ = env.run(t, "runs", "verify", runID)
	}
	assertDrift := func(want ...models.PublicFeedVerificationDrift) {
		t.Helper()
		status, err := gwremote.GatewayPublisherStatus(run.ctx, run.fileSvc, run.cfg)
		require.NoError(t, err)
		encoded, err := json.Marshal(status)
		require.NoError(t, err)
		var printed models.PublicPublisherStatus
		require.NoError(t, json.Unmarshal(encoded, &printed))
		assert.Equal(t, want, printed.FeedDrift)
	}
	driftOf := func(local, feed string) models.PublicFeedVerificationDrift {
		return models.PublicFeedVerificationDrift{RunID: runID, LocalStatus: local, FeedVerifierState: feed}
	}

	assertDrift() // No report yet: nothing to compare.
	localVerify(original)
	assertDrift(driftOf("verified", "not_run"))
	localVerify(rejected)
	assertDrift(driftOf("verify_failed", "not_run"))

	require.NoError(t, store.SaveAssignmentResult(run.ctx, original))
	out, err = verifyCLI(t, env, runID)
	require.NoError(t, err, out)
	assertDrift() // verified and passed agree.
	localVerify(rejected)
	assertDrift(driftOf("verify_failed", "passed"))

	_, err = verifyCLI(t, env, runID)
	require.ErrorIs(t, err, constants.ErrEvalRunVerificationFailed)
	assertDrift() // verify_failed and failed agree.
	localVerify(original)
	assertDrift(driftOf("verified", "failed"))
}

// A witness flag stricter than the run's frozen policy could otherwise post a
// public failure for a run that passes at its own policy.
func TestRunsVerifyPublish_RefusesWitnessFlagStricterThanFrozenPolicy(t *testing.T) {
	env, run := newPublicationEnv(t, severityReplies(3))
	createPublicationCampaign(t, run, "pub-strict", 3)
	executeCampaign(t, env, "pub-strict")
	runID := onlyRunID(t, run)
	before := mirrorHighWater(t)
	for _, flag := range []string{"--require-provenance", "--require-observation"} {
		_, err := env.run(t, "runs", "verify", runID, "--publish", flag)
		require.ErrorIs(t, err, constants.ErrEvaluationFlagsInvalid, flag)
	}
	assert.Equal(t, before, mirrorHighWater(t), "a refused verify publishes nothing")
}

// The live false pass was a formation run, and the leaderboard ranks formations from
// these same summary fields, so the downgrade must hold on the system lane too.
func TestRunsVerifyPublish_FailureSupersedesEarlierPassOnFormationRun(t *testing.T) {
	variants := evaluation.FormationCatalogFixtureVariants(func(tag string) string { return fixtures.ScriptedModelDigest(t, tag) })
	tags := make([]string, 0, len(variants))
	for _, variant := range variants {
		tags = append(tags, variant.GetServedModelTag())
	}
	replies := make([]fixtures.OllamaReply, 64)
	for i := range replies {
		replies[i].Content = "severity: high"
	}
	env, run := newPublicationEnvWithOptions(t, fixtures.EvalStackOptions{
		Posture: constants.PostureDoctrine, EnablePublicSpectator: true, ModelTags: tags, Replies: withScriptedJudgeVerdicts(replies),
	})
	suite := evaluation.DefaultScenarioSuite()
	suite.ID = "pub-formation-suite"
	suite.Version = evaluation.SuiteSchemaVersion
	for _, scenario := range evaluation.DefaultScenarioSuite().Scenarios {
		if scenario.ScenarioID == "instruction-classify-severity" {
			scenario.EligibleRoles = []string{"primary"}
			suite.Scenarios = []evaluation.SuiteScenario{scenario}
			break
		}
	}
	require.Len(t, suite.Scenarios, 1)
	require.NoError(t, evaluation.NewStore(run.fileSvc).CreateSuite(run.ctx, suite))
	writeTestFrozenInventory(t, env.root, append(variants, scriptedJudgeVariant(t))...)
	out, err := env.run(t, "campaigns", "create", "pub-formation", "--formations", "heterogeneous-0000", "--suite", suite.ID)
	require.NoError(t, err, out)
	out, err = env.run(t, "runs", "start", "pub-formation", "--verify=false")
	require.NoError(t, err, out)
	runID := onlyRunID(t, run)
	assignments := runAssignments(t, run, runID)
	require.NotEmpty(t, assignments)
	for _, assignment := range assignments {
		require.True(t, evaluation.IsHeterogeneousAssignment(assignment), "the run executes on the system lane")
	}

	out, err = verifyCLI(t, env, runID)
	require.NoError(t, err, out)
	assertFormationSummaryQualifies(t, true)

	store := evaluation.NewStore(run.fileSvc)
	results, err := store.LoadAssignmentResults(run.ctx, runID, assignments)
	require.NoError(t, err)
	var original *evalv1.EvaluationAssignmentResult
	for _, result := range results {
		original = proto.Clone(result).(*evalv1.EvaluationAssignmentResult)
		result.LifecycleStatus = evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_INVALID_EVIDENCE
		result.ResultDigest, err = evaluation.ComputeAssignmentResultDigest(result)
		require.NoError(t, err)
		require.NoError(t, store.SaveAssignmentResult(run.ctx, result))
		break
	}
	out, err = verifyCLI(t, env, runID)
	require.ErrorIs(t, err, constants.ErrEvalRunVerificationFailed, out)
	assertFormationSummaryQualifies(t, false)

	require.NoError(t, store.SaveAssignmentResult(run.ctx, original))
	out, err = verifyCLI(t, env, runID)
	require.NoError(t, err, out)
	assertFormationSummaryQualifies(t, true)
}

// assertFormationSummaryQualifies reads the fields the leaderboard's formation
// qualification requires from the latest evaluation summary.
func assertFormationSummaryQualifies(t *testing.T, qualifies bool) {
	t.Helper()
	var latest struct {
		VerifierState string `json:"verifier_state"`
		QualityState  string `json:"quality_state"`
	}
	for _, projection := range mirrorProjections(t) {
		var payload struct {
			Kind string `json:"kind"`
		}
		require.NoError(t, json.Unmarshal(projection.record, &payload))
		if payload.Kind == "evaluation_summary" {
			require.NoError(t, json.Unmarshal(projection.record, &latest))
		}
	}
	if qualifies {
		assert.Equal(t, "passed", latest.VerifierState)
		assert.Equal(t, "exploratory_verified", latest.QualityState)
		return
	}
	assert.Equal(t, "failed", latest.VerifierState)
	assert.NotEqual(t, "exploratory_verified", latest.QualityState)
}

// assertPublishedOutcome reads the mirror's latest revision of each record the
// leaderboard and assignment views read. An empty quality state asserts only that
// no model summary still claims a verified quality state.
func assertPublishedOutcome(t *testing.T, assignments []*evalv1.EvaluationAssignment, verifierState, verificationStatus, qualityState string) {
	t.Helper()
	rowVerdict := evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL.String()
	if verifierState == "passed" {
		rowVerdict = evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS.String()
	}
	assert.Equal(t, verifierState, latestSummary(t).VerifierState, "summary verifier_state")
	rows := map[string]publicationAssignmentRow{}
	modelStates := map[string]string{}
	for _, projection := range mirrorProjections(t) {
		var row publicationAssignmentRow
		require.NoError(t, json.Unmarshal(projection.record, &row))
		switch {
		case row.Kind == "model_summary":
			modelStates[row.VariantID+":"+row.Role] = row.QualityState
		case row.AssignmentID != "" && row.VerificationStatus != "":
			rows[row.AssignmentID] = row
		}
	}
	require.NotEmpty(t, modelStates)
	for key, state := range modelStates {
		if qualityState == "" {
			assert.NotEqual(t, "exploratory_verified", state, "model summary %s", key)
		} else {
			assert.Equal(t, qualityState, state, "model summary %s", key)
		}
	}
	for _, assignment := range assignments {
		row, ok := rows[assignment.GetAssignmentId()]
		require.True(t, ok, "assignment %s has a published row", assignment.GetAssignmentId())
		assert.Equal(t, verificationStatus, row.VerificationStatus, "assignment %s verification_status", assignment.GetAssignmentId())
		require.NotNil(t, row.Metadata, "assignment %s verification_metadata", assignment.GetAssignmentId())
		assert.Equal(t, rowVerdict, row.Metadata.VerifierState, "assignment %s metadata verifier_state", assignment.GetAssignmentId())
		assert.Equal(t, evalv1.PublicVerificationProvenance_PUBLIC_VERIFICATION_PROVENANCE_BOUND.String(), row.Metadata.Provenance, "assignment %s metadata provenance", assignment.GetAssignmentId())
	}
}

// publicationAssignmentRow is the subset of an assignment or model record the
// outcome assertions read.
type publicationAssignmentRow struct {
	Kind               string                       `json:"kind"`
	VariantID          string                       `json:"variant_id"`
	Role               string                       `json:"role"`
	QualityState       string                       `json:"quality_state"`
	AssignmentID       string                       `json:"assignment_id"`
	VerificationStatus string                       `json:"verification_status"`
	Metadata           *publicationVerificationMeta `json:"verification_metadata"`
}

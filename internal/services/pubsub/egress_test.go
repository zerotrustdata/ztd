// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package pubsub

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	pubsubtest "github.com/zerotrustdata/ztd/internal/services/pubsub/pubsubtest"
	"github.com/zerotrustdata/ztd/internal/services/scrubbing"
	"github.com/zerotrustdata/ztd/internal/services/storage/storagetest"
	"github.com/zerotrustdata/ztd/internal/testutil"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const egressSecretEmail = "alice@example.com"

func newEgressScrubber(t *testing.T, enabled bool) *scrubbing.ScrubbingService {
	t.Helper()
	cfg := scrubbing.DefaultConfig()
	cfg.Enabled = enabled
	svc, err := scrubbing.NewScrubbingService(context.Background(), cfg, testutil.NewTestLogger(), storagetest.NewTestTokenStore())
	require.NoError(t, err)
	return svc
}

func TestEgressScrubbed_WithholdsContentWithoutActiveScrubber(t *testing.T) {
	payload := &operatorv1.FetchLogsResult{ExecutionId: "exec-1", Stdout: egressSecretEmail}

	tests := []struct {
		name string
		s    *scrubbing.ScrubbingService
	}{
		{name: "nil scrubber", s: nil},
		{name: "disabled scrubber", s: newEgressScrubber(t, false)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := egressScrubbed(context.Background(), payload, tt.s)
			require.ErrorIs(t, err, constants.ErrEgressScrubberUnavailable)
			assert.Nil(t, out)
		})
	}
}

func TestEgressScrubbed_ScrubsContentFields(t *testing.T) {
	s := newEgressScrubber(t, true)
	payload := &operatorv1.FetchFileDiffResult{
		Success:     true,
		ExecutionId: "exec-2",
		Diff: &operatorv1.FileDiffEntry{
			Id:          "diff-1",
			FilePath:    "/srv/" + egressSecretEmail,
			DiffStat:    "1 insertion for " + egressSecretEmail,
			DiffContent: "+contact " + egressSecretEmail,
		},
	}

	out, err := egressScrubbed(context.Background(), payload, s)
	require.NoError(t, err)
	scrubbed, ok := out.(*operatorv1.FetchFileDiffResult)
	require.True(t, ok)

	assert.NotContains(t, scrubbed.Diff.FilePath, egressSecretEmail)
	assert.NotContains(t, scrubbed.Diff.DiffStat, egressSecretEmail)
	assert.NotContains(t, scrubbed.Diff.DiffContent, egressSecretEmail)
	assert.Equal(t, "exec-2", scrubbed.ExecutionId, "identifiers are not content")
	assert.Equal(t, "diff-1", scrubbed.Diff.Id)

	// The caller's payload is not mutated: scrubbing works on a clone.
	assert.Equal(t, "+contact "+egressSecretEmail, payload.Diff.DiffContent)
}

func TestEgressScrubbed_PassesContentFreePayloadsThrough(t *testing.T) {
	payload := &operatorv1.HeartbeatResult{}
	out, err := egressScrubbed(context.Background(), payload, nil)
	require.NoError(t, err)
	assert.True(t, proto.Equal(payload, out))
}

func TestPublishLFAATypedResponseTo_WithholdsRawLogsWithoutScrubber(t *testing.T) {
	cfg := testutil.NewTestConfig(t)
	logger := testutil.NewTestLogger()
	client := pubsubtest.NewMockOperatorPubSubClient()

	msg := &PubSubCommandMessage{
		ID:                "msg-egress-1",
		EventType:         constants.Event.Operator.FetchLogs.Requested,
		OperatorSessionID: "session-1",
		Payload:           mustMarshalProto(t, &operatorv1.FetchLogsRequested{ExecutionId: "exec-9"}),
	}
	payload := &operatorv1.FetchLogsResult{ExecutionId: "exec-9", Stdout: egressSecretEmail}

	publishLFAATypedResponseTo(context.Background(), client, cfg, logger, msg,
		constants.Event.Operator.FetchLogs.Completed, payload, nil, nil)

	published := client.LastPublished()
	require.NotNil(t, published)
	assert.False(t, strings.Contains(string(published.Data), egressSecretEmail), "raw output must not be published")
	assert.Contains(t, string(published.Data), string(constants.Event.Operator.FetchLogs.Failed))
	assert.NotContains(t, string(published.Data), string(constants.Event.Operator.FetchLogs.Completed))
}

func TestEgressFailureEvent_CoversContentPayloads(t *testing.T) {
	assert.Equal(t, constants.Event.Operator.FetchLogs.Failed, egressFailureEvent(constants.Event.Operator.FetchLogs.Completed, &operatorv1.FetchLogsResult{}))
	assert.Equal(t, constants.Event.Operator.FetchFileDiff.Failed, egressFailureEvent(constants.Event.Operator.FetchFileDiff.Completed, &operatorv1.FetchFileDiffResult{}))
	assert.Equal(t, constants.Event.Operator.FsRead.Failed, egressFailureEvent(constants.Event.Operator.FsRead.Completed, &operatorv1.FsReadResult{}))
	assert.Equal(t, constants.Event.Operator.Command.Failed, egressFailureEvent(constants.Event.Operator.Command.Completed, &operatorv1.CommandResult{}))
	assert.Equal(t, constants.EventType(""), egressFailureEvent(constants.Event.Operator.Heartbeat, &operatorv1.HeartbeatResult{}))
}

// A cancellation result is a CommandResult, so the outcome event selects its typed failure.
func TestEgressFailureEvent_CancellationMapsToCancelFailed(t *testing.T) {
	assert.Equal(t, constants.Event.Operator.Command.CancelFailed,
		egressFailureEvent(constants.Event.Operator.Command.Cancelled, &operatorv1.CommandResult{}))
}

func TestPubSubResultsService_WithheldCancellationPublishesCancelFailed(t *testing.T) {
	db := pubsubtest.NewMockOperatorPubSubClient()
	cfg := testutil.NewTestConfig(t)
	// A nil scrubber withholds the CommandResult error text, so the cancellation must publish its typed failure.
	svc, err := NewPubSubResultsService(cfg, testutil.NewTestLogger(), db, nil)
	require.NoError(t, err)

	originalMsg := &PubSubCommandMessage{
		ID:                "msg-cancel-1",
		EventType:         constants.Event.Operator.Command.CancelRequested,
		CaseID:            "case-cancel-1",
		OperatorSessionID: "web-session-cancel-1",
	}
	result := &operatorv1.CommandResult{
		ExecutionId: "exec-cancel-1",
		Status:      operatorv1.ExecutionStatus_EXECUTION_STATUS_CANCELLED,
		Error:       "Command cancelled by user",
	}

	// The typed failure is published, and the withholding is still reported to the caller.
	require.ErrorIs(t, svc.PublishCancellationResult(context.Background(), result, originalMsg), constants.ErrEgressScrubberUnavailable)

	env := mustUnmarshalGovernanceEnvelope(t, requireLastPublishedUniversal(t, db))
	assert.Equal(t, string(constants.Event.Operator.Command.CancelFailed), env.EventType)
	assert.NotEqual(t, string(constants.Event.Operator.Command.Cancelled), env.EventType)
}

// egressResultPayloadTypes enumerates every protobuf message that is published as an Operator result
// payload: the types mapped by mapProtoToPayloadType plus the publish-only types (shutdown
// acknowledgement, inference progress and completion, observation completions). Adding a result type
// without classifying it in egressContentFields fails TestEgressContentFields_ClassifiesEveryResultType.
func egressResultPayloadTypes() map[string]struct {
	msg   proto.Message
	class egressClass
} {
	return map[string]struct {
		msg   proto.Message
		class egressClass
	}{
		"CommandResult":                        {&operatorv1.CommandResult{}, egressContent},
		"ExecutionStatusUpdate":                {&operatorv1.ExecutionStatusUpdate{}, egressContent},
		"FileEditResult":                       {&operatorv1.FileEditResult{}, egressContent},
		"FsListResult":                         {&operatorv1.FsListResult{}, egressContent},
		"FsGrepResult":                         {&operatorv1.FsGrepResult{}, egressContent},
		"FsReadResult":                         {&operatorv1.FsReadResult{}, egressContent},
		"PortCheckResult":                      {&operatorv1.PortCheckResult{}, egressContent},
		"OllamaModelInventoryResult":           {&operatorv1.OllamaModelInventoryResult{}, egressContent},
		"OllamaModelResidencyResult":           {&operatorv1.OllamaModelResidencyResult{}, egressContent},
		"OllamaModelReleaseResult":             {&operatorv1.OllamaModelReleaseResult{}, egressContent},
		"FetchLogsResult":                      {&operatorv1.FetchLogsResult{}, egressContent},
		"FetchFileDiffResult":                  {&operatorv1.FetchFileDiffResult{}, egressContent},
		"FetchHistoryResult":                   {&operatorv1.FetchHistoryResult{}, egressContent},
		"FetchFileHistoryResult":               {&operatorv1.FetchFileHistoryResult{}, egressContent},
		"RestoreFileResult":                    {&operatorv1.RestoreFileResult{}, egressContent},
		"ShutdownRequested":                    {&operatorv1.ShutdownRequested{}, egressContent},
		"InferenceProgressEvent":               {&operatorv1.InferenceProgressEvent{}, egressDigestBound},
		"HeartbeatResult":                      {&operatorv1.HeartbeatResult{}, egressNoContent},
		"InferenceCompletion":                  {&operatorv1.InferenceCompletion{}, egressDigestBound},
		"ProviderBoundaryObservationCompleted": {&evalv1.ProviderBoundaryObservationCompleted{}, egressDigestBound},
		"ModelProvenanceObservationCompleted":  {&evalv1.ModelProvenanceObservationCompleted{}, egressDigestBound},
	}
}

func TestEgressContentFields_ClassifiesEveryResultType(t *testing.T) {
	for name, tt := range egressResultPayloadTypes() {
		t.Run(name, func(t *testing.T) {
			fields, class, err := egressContentFields(tt.msg)
			require.NoError(t, err, "result type %s must be classified", name)
			assert.Equal(t, tt.class, class)
			if class != egressContent {
				assert.Empty(t, fields, "non-content type %s must not list content fields", name)
			}
		})
	}
}

func TestEgressScrubbed_UnclassifiedTypeFailsLoudly(t *testing.T) {
	// A request message is not a result payload and has no classification.
	payload := &operatorv1.CheckPortRequested{Host: egressSecretEmail, Port: 22}
	s := newEgressScrubber(t, true)

	out, err := egressScrubbed(context.Background(), payload, s)
	require.ErrorIs(t, err, constants.ErrEgressPayloadUnclassified)
	assert.Nil(t, out)

	_, _, err = egressContentFields(payload)
	require.ErrorIs(t, err, constants.ErrEgressPayloadUnclassified)
}

func TestEgressScrubbed_ScrubsFileHistoryAndRestoreResults(t *testing.T) {
	s := newEgressScrubber(t, true)

	history := &operatorv1.FetchFileHistoryResult{
		Success:  true,
		FilePath: "/srv/" + egressSecretEmail + "/notes.txt",
		History: []*operatorv1.FileHistoryEntry{
			{CommitHash: "abc123", Timestamp: "2026-10-12", Message: "edit for " + egressSecretEmail},
		},
	}
	out, err := egressScrubbed(context.Background(), history, s)
	require.NoError(t, err)
	scrubbedHistory := out.(*operatorv1.FetchFileHistoryResult)
	assert.NotContains(t, scrubbedHistory.FilePath, egressSecretEmail)
	assert.NotContains(t, scrubbedHistory.History[0].Message, egressSecretEmail)
	assert.Equal(t, "abc123", scrubbedHistory.History[0].CommitHash, "commit hashes are identifiers")
	assert.Contains(t, history.History[0].Message, egressSecretEmail, "caller payload is not mutated")

	restore := &operatorv1.RestoreFileResult{
		Success:     true,
		FilePath:    "/srv/" + egressSecretEmail + "/restored.txt",
		CommitHash:  "def456",
		ExecutionId: "exec-restore",
	}
	out, err = egressScrubbed(context.Background(), restore, s)
	require.NoError(t, err)
	scrubbedRestore := out.(*operatorv1.RestoreFileResult)
	assert.NotContains(t, scrubbedRestore.FilePath, egressSecretEmail)
	assert.Equal(t, "def456", scrubbedRestore.CommitHash)

	_, err = egressScrubbed(context.Background(), restore, nil)
	require.ErrorIs(t, err, constants.ErrEgressScrubberUnavailable)
}

// Progress is scrubbed at the source (inference stream segmenter), so egress does not rewrite it and
// does not require a scrubber: a rewrite here would break the OutputHash binding.
func TestEgressScrubbed_InferenceProgressIsDigestBoundAndPassesThrough(t *testing.T) {
	progress := &operatorv1.InferenceProgressEvent{
		ProviderAttemptId: "attempt-1",
		Sequence:          1,
		Parts: []*operatorv1.InferenceResponsePart{
			{Part: &operatorv1.InferenceResponsePart_Text{Text: "{{UEI_3f9a0c12be47_1}} "}},
		},
	}
	for name, s := range map[string]*scrubbing.ScrubbingService{
		"enabled scrubber": newEgressScrubber(t, true),
		"nil scrubber":     nil,
	} {
		t.Run(name, func(t *testing.T) {
			out, err := egressScrubbed(context.Background(), progress, s)
			require.NoError(t, err)
			assert.True(t, proto.Equal(progress, out.(*operatorv1.InferenceProgressEvent)))
		})
	}
}

func TestPublishLFAAError_ScrubsErrorTextOrPublishesGenericText(t *testing.T) {
	rawError := "failed to retrieve execution: /home/alice/" + egressSecretEmail + "/out.log"
	msg := &PubSubCommandMessage{
		ID:                "msg-error-text",
		EventType:         constants.Event.Operator.FetchLogs.Requested,
		OperatorSessionID: "session-1",
		Payload:           mustMarshalProto(t, &operatorv1.FetchLogsRequested{ExecutionId: "exec-9"}),
	}

	t.Run("enabled scrubber redacts the error text", func(t *testing.T) {
		client := pubsubtest.NewMockOperatorPubSubClient()
		publishLFAAErrorTo(context.Background(), client, testutil.NewTestConfig(t), testutil.NewTestLogger(), msg,
			constants.Event.Operator.FetchLogs.Failed, rawError, nil, newEgressScrubber(t, true))
		published := client.LastPublished()
		require.NotNil(t, published)
		assert.NotContains(t, string(published.Data), egressSecretEmail)
		assert.Contains(t, string(published.Data), string(constants.Event.Operator.FetchLogs.Failed))
	})

	for _, tt := range []struct {
		name string
		s    *scrubbing.ScrubbingService
	}{
		{name: "nil scrubber", s: nil},
		{name: "disabled scrubber", s: newEgressScrubber(t, false)},
	} {
		t.Run(tt.name+" publishes generic text", func(t *testing.T) {
			client := pubsubtest.NewMockOperatorPubSubClient()
			publishLFAAErrorTo(context.Background(), client, testutil.NewTestConfig(t), testutil.NewTestLogger(), msg,
				constants.Event.Operator.FetchLogs.Failed, rawError, nil, tt.s)
			published := client.LastPublished()
			require.NotNil(t, published)
			assert.NotContains(t, string(published.Data), egressSecretEmail)
			assert.NotContains(t, string(published.Data), "/home/alice")
			assert.Contains(t, string(published.Data), constants.ErrEgressScrubberUnavailable.Error())
		})
	}
}

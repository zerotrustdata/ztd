// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/tribunal"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// TestConsensusVoteReachesThePublisherCreatedAtSpectatorStart builds the
// spectator the way Gateway build does, binds the vote feed before Start, and
// requires a consensus vote to land on the mirror. Publisher() is nil until
// Start, so a build-time snapshot never publishes.
func TestConsensusVoteReachesThePublisherCreatedAtSpectatorStart(t *testing.T) {
	privatePort := mustFreePort(t)
	publicPort := mustFreePort(t)
	fileSvc := newProducerFileSvc(t)
	logger := testutil.NewTestLogger()
	ks := newTestKeystore(t, fileSvc, logger)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	runtime, err := NewPublicSpectatorRuntime(PublicSpectatorConfig{
		Enabled:               true,
		PrivateListenAddress:  "127.0.0.1:" + privatePort,
		PublicListenAddress:   "127.0.0.1:" + publicPort,
		ExplorerListenAddress: "",
	}, fileSvc, ks, logger)
	require.NoError(t, err)
	require.Nil(t, runtime.Publisher())

	env, _ := tribunalTestEnvelope(t)
	env.Posture = string(constants.PostureConsensus)
	env.InvestigationId = "run-live-vote"
	env, _ = hashTribunalTestEnvelope(t, env)
	binding := tribunalTestBinding()
	recorder := &fakeVoteRecorder{}
	deliberator := newRecordingTribunalTestDeliberator(t, binding, &fakeInferer{}, newFakeSigner(t, binding.MemberAppIDs...), recorder)
	// Gateway build binds the feed here, before Start creates the publisher.
	bindTribunalVoteFeed(deliberator, runtime)
	require.NoError(t, deliberator.recordVote(ctx, env, binding, binding.MemberAppIDs[0], memberVote{
		answer: tribunal.ConsensusVoteAnswer{Decision: tribunal.ConsensusVoteApprove, Reason: "before start"},
	}))

	require.NoError(t, runtime.Start(ctx))
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		require.NoError(t, runtime.Stop(stopCtx))
	})
	require.NotNil(t, runtime.Publisher())

	memberAppID := binding.MemberAppIDs[1]
	require.NoError(t, deliberator.recordVote(ctx, env, binding, memberAppID, memberVote{
		answer: tribunal.ConsensusVoteAnswer{Decision: tribunal.ConsensusVoteApprove, Reason: "read-only query"},
	}))

	page := spectatorHistory(t, "http://127.0.0.1:"+publicPort+"/history?kind=stage_updated&limit=20")
	var labels []string
	for _, item := range page.Items {
		label, ok := item.StringField("stage_label")
		if ok && strings.HasPrefix(label, tribunalVoteStagePrefix) {
			labels = append(labels, label)
		}
	}
	require.Len(t, labels, 1)
	var published models.ConsensusVoteRecord
	require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(labels[0], tribunalVoteStagePrefix)), &published))
	recorded := recorder.records(t)
	require.Len(t, recorded, 2)
	require.Equal(t, recorded[1], published)
	require.Equal(t, memberAppID, published.MemberAppID)
	require.Equal(t, "read-only query", published.Reason)
}

func spectatorHistory(t *testing.T, url string) models.PublicFeedCursorPage {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, url, nil)
	require.NoError(t, err)
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode, string(body))
	var page models.PublicFeedCursorPage
	require.NoError(t, json.Unmarshal(body, &page))
	return page
}

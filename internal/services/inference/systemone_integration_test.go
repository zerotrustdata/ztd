// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package inference

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/testutil"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestSystemOneOllamaTypedTransportAndDigestPin(t *testing.T) {
	digest := strings.Repeat("a", 64)
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ollama/api/tags":
			fmt.Fprintf(w, `{"models":[{"name":"tev1:0.8b","digest":"sha256:%s"}]}`, digest)
		case "/ollama/v1/systemone":
			posts++
			fmt.Fprint(w, `{"model":"tev1:0.8b","answers":{"safe":{"type":"noul","noul":0.99},"action":{"type":"choice","choice":"read","probabilities":{"read":0.9,"write":0.1},"confidence":0.8},"risk":{"type":"score","score":0.2,"probabilities":{"0":0.8,"1":0.2},"confidence":0.6}},"usage":{"input_tokens":15,"output_tokens":3}}`)
		default:
			t.Errorf("unexpected provider path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	backend, err := NewOllamaBackend(server.URL+"/ollama", testutil.NewTestLogger())
	require.NoError(t, err)
	input := decisionInput()
	input.Questions = append(input.Questions,
		&operatorv1.InferenceDecisionQuestion{Name: "action", Instructions: "Choose action", Kind: &operatorv1.InferenceDecisionQuestion_Choice{Choice: &operatorv1.InferenceChoiceQuestion{Criteria: []*operatorv1.InferenceDecisionCriterion{{Label: "read", Description: "Read"}, {Label: "write", Description: "Write"}}}}},
		&operatorv1.InferenceDecisionQuestion{Name: "risk", Instructions: "Score risk", Kind: &operatorv1.InferenceDecisionQuestion_Score{Score: &operatorv1.InferenceScoreQuestion{Criteria: []string{"low", "high"}}}})
	req := models.DecisionRequest{Input: input, Model: "tev1:0.8b", ModelDigest: digest, KeepAlive: "-1"}
	resp, err := backend.Decide(t.Context(), req)
	require.NoError(t, err)
	require.Len(t, resp.Decision.Answers, 3)
	require.InDelta(t, .2, resp.Decision.Answers[2].GetScore().Score, 0)
	require.Equal(t, digest, resp.ServedModelDigest)
	require.EqualValues(t, 18, resp.TotalTokens)
	req.ModelDigest = strings.Repeat("b", 64)
	_, err = backend.Decide(t.Context(), req)
	require.ErrorIs(t, err, constants.ErrInferenceModelDigestMismatch)
	require.Equal(t, 1, posts)
}
func TestSystemOneOllamaRejectsMalformedAndErrorResponses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"missing model", 200, `{"answers":{"safe":{"type":"noul","noul":1}},"usage":{"input_tokens":1,"output_tokens":1}}`, constants.ErrInferenceDecisionResponseInvalid},
		{"missing answer", 200, `{"model":"tev1:0.8b","answers":{},"usage":{"input_tokens":1,"output_tokens":1}}`, constants.ErrInferenceDecisionResponseInvalid},
		{"missing probability", 200, `{"model":"tev1:0.8b","answers":{"safe":{"type":"noul"}},"usage":{"input_tokens":1,"output_tokens":1}}`, constants.ErrInferenceDecisionResponseInvalid},
		{"wrong kind", 200, `{"model":"tev1:0.8b","answers":{"safe":{"type":"choice","noul":1}},"usage":{"input_tokens":1,"output_tokens":1}}`, constants.ErrInferenceDecisionResponseInvalid},
		{"duplicate answer", 200, `{"model":"tev1:0.8b","answers":{"safe":{"type":"noul","noul":1},"safe":{"type":"noul","noul":0}},"usage":{"input_tokens":1,"output_tokens":1}}`, constants.ErrInferenceDecisionResponseInvalid},
		{"out of range", 200, `{"model":"tev1:0.8b","answers":{"safe":{"type":"noul","noul":2}},"usage":{"input_tokens":1,"output_tokens":1}}`, constants.ErrInferenceDecisionResponseInvalid},
		{"not found", 404, `{"error":"do not echo this"}`, constants.ErrInferenceModelNotFound},
		{"context rejected", 400, `{"error":"do not echo this"}`, constants.ErrInferenceDecisionRequestInvalid},
		{"too large", 413, `{"error":"do not echo this"}`, constants.ErrInferenceRequestTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.body) }))
			defer server.Close()
			backend, err := NewOllamaBackend(server.URL, testutil.NewTestLogger())
			require.NoError(t, err)
			_, err = backend.Decide(t.Context(), models.DecisionRequest{Input: decisionInput(), Model: "tev1:0.8b"})
			require.ErrorIs(t, err, tc.want)
			require.NotContains(t, err.Error(), "do not echo this")
		})
	}
}

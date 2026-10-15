// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package drive

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubClient sends one connection per request. The default transport can leave an
// unused connection in StateNew, which makes the stub's Shutdown wait out its timeout.
var stubClient = &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}

// startStub starts a stub on an ephemeral loopback port and closes it when the test ends.
func startStub(t *testing.T) *A2AStub {
	t.Helper()
	s, err := StartA2AStub("127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		assert.NoError(t, s.Close(ctx))
	})
	return s
}

// postDownstream posts body to the stub the way the Gateway does and returns the status.
func postDownstream(t *testing.T, s *A2AStub, body string) int {
	t.Helper()
	resp, err := stubClient.Post(s.URL(), "application/json", bytes.NewBufferString(body))
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()
	return resp.StatusCode
}

func TestA2AStubReturnsCallBetweenBeginAndEnd(t *testing.T) {
	s := startStub(t)
	s.Begin("attempt-1")
	require.Equal(t, http.StatusOK, postDownstream(t, s, `{"skill_name":"ztd-conformance","payload":{"marker":"m"}}`))
	calls := s.End("attempt-1")

	require.Len(t, calls, 1)
	assert.Equal(t, "ztd-conformance", calls[0].GetSkillName())
	assert.JSONEq(t, `{"marker":"m"}`, calls[0].GetPayloadJson())
}

func TestA2AStubReturnsCallOnceThenNothing(t *testing.T) {
	s := startStub(t)
	s.Begin("attempt-1")
	require.Equal(t, http.StatusOK, postDownstream(t, s, `{"skill_name":"ztd-conformance","payload":{}}`))

	assert.Len(t, s.End("attempt-1"), 1)
	assert.Empty(t, s.End("attempt-1"), "a second End must not return the same call again")
}

func TestA2AStubKeepsStrayCallForNextEnd(t *testing.T) {
	s := startStub(t)
	require.Equal(t, http.StatusOK, postDownstream(t, s, `{"skill_name":"ztd-conformance","payload":{"marker":"stray"}}`))

	s.Begin("attempt-2")
	require.Equal(t, http.StatusOK, postDownstream(t, s, `{"skill_name":"ztd-conformance","payload":{"marker":"own"}}`))
	calls := s.End("attempt-2")

	require.Len(t, calls, 2, "the stray call from before Begin must be returned by the next End")
	assert.JSONEq(t, `{"marker":"stray"}`, calls[0].GetPayloadJson())
	assert.JSONEq(t, `{"marker":"own"}`, calls[1].GetPayloadJson())
}

func TestA2AStubCallAfterEndIsStrayNotAttributed(t *testing.T) {
	s := startStub(t)
	s.Begin("attempt-3")
	assert.Empty(t, s.End("attempt-3"))

	require.Equal(t, http.StatusOK, postDownstream(t, s, `{"skill_name":"ztd-conformance","payload":{}}`))
	calls := s.End("attempt-4")
	require.Len(t, calls, 1, "a call that arrives after End is returned by the next End, not dropped")
}

func TestA2AStubRejectsMalformedBodyAndRecordsNothing(t *testing.T) {
	cases := map[string]string{
		"truncated JSON":                     `{"skill_name":`,
		"unknown field":                      `{"skill_name":"ztd-conformance","payload":{},"extra":true}`,
		"wrong field type":                   `{"skill_name":3,"payload":{}}`,
		"trailing data after the JSON value": `{"skill_name":"ztd-conformance","payload":{}} {}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			s := startStub(t)
			s.Begin("attempt-5")
			assert.Equal(t, http.StatusBadRequest, postDownstream(t, s, body))
			assert.Empty(t, s.End("attempt-5"))
		})
	}
}

func TestA2AStubRecordsWrongSkill(t *testing.T) {
	s := startStub(t)
	s.Begin("attempt-6")
	require.Equal(t, http.StatusOK, postDownstream(t, s, `{"skill_name":"not-the-conformance-skill","payload":{}}`))
	calls := s.End("attempt-6")

	require.Len(t, calls, 1, "the stub records any skill; the verifier decides whether it is allowed")
	assert.Equal(t, "not-the-conformance-skill", calls[0].GetSkillName())
}

func TestA2AStubAnswersSuccessShape(t *testing.T) {
	s := startStub(t)
	resp, err := stubClient.Post(s.URL(), "application/json", bytes.NewBufferString(`{"skill_name":"ztd-conformance","payload":{}}`))
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	var got a2aStubResult
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	assert.Equal(t, "ok", got.Result)
}

func TestA2AStubRejectsNonPOST(t *testing.T) {
	s := startStub(t)
	s.Begin("attempt-7")
	resp, err := stubClient.Get(s.URL())
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()
	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
	assert.Empty(t, s.End("attempt-7"))
}

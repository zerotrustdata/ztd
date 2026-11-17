// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package tribunal

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/jsonschema"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/testutil"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// compileCanonicalSchema requires schema to be the canonical JSON the
// Inference Operator accepts, and to compile.
func compileCanonicalSchema(t *testing.T, schema string) *jsonschema.Schema {
	t.Helper()
	var decoded any
	require.NoError(t, json.Unmarshal([]byte(schema), &decoded))
	canonical, err := json.Marshal(decoded)
	require.NoError(t, err)
	require.Equal(t, string(canonical), schema, "schema must be canonical JSON")
	compiled, err := jsonschema.NewCompiler().Compile([]byte(schema))
	require.NoError(t, err)
	return compiled
}

func TestResponseSchemasConstrainEachRoleToItsDecoderShape(t *testing.T) {
	tests := []struct {
		name     string
		schema   string
		accept   []string
		rejected []string
	}{
		{
			name:   "generation",
			schema: generationJSONSchema,
			accept: []string{`{"command":"ss","args":["-tlpn"]}`, `{"command":"uptime","args":[]}`},
			rejected: []string{
				`{"command":"ss","args":["-l"],"timeout":30}`,
				`{"command":"ss","args":["-l"],"working_dir":"/srv"}`,
				`{"command":"ss"}`,
			},
		},
		{
			name:   "marshal",
			schema: marshalJSONSchema,
			accept: []string{`{"risk":"LOW","rationale":"read-only","feedback":""}`, `{"risk":"HIGH","rationale":"deletes /","feedback":"scope the path"}`},
			rejected: []string{
				`{"risk":"low","rationale":"x","feedback":""}`,
				`{"risk":"LOW","rationale":"x"}`,
				`{"risk":"LOW"}`,
				`{"risk":"LOW","rationale":"x","feedback":"","score":1}`,
			},
		},
		{
			name:   "auditor with alternatives",
			schema: auditorJSONSchema([]string{"cluster_b", "cluster_c"}),
			accept: []string{
				`{"verdict":"APPROVE","revised_arguments":null,"swap_to_cluster":null,"rationale":"x"}`,
				`{"verdict":"REVISE","revised_arguments":{"command":"ss","args":["-tlpn"]},"swap_to_cluster":null,"rationale":"x"}`,
				`{"verdict":"SWAP","revised_arguments":null,"swap_to_cluster":"cluster_c","rationale":"x"}`,
			},
			rejected: []string{
				`{"verdict":"APPROVE","rationale":"x"}`,
				`{"verdict":"APPROVE","revised_arguments":null,"rationale":"x"}`,
				`{"verdict":"REVISE","revised_arguments":{"command":"ss","args":[],"timeout":30},"swap_to_cluster":null,"rationale":"x"}`,
				`{"verdict":"SWAP","revised_arguments":null,"swap_to_cluster":"cluster_a","rationale":"x"}`,
				`{"verdict":"VETO","revised_arguments":null,"swap_to_cluster":null,"rationale":"x"}`,
			},
		},
		{
			name:   "auditor without alternatives",
			schema: auditorJSONSchema(nil),
			accept: []string{`{"verdict":"APPROVE","revised_arguments":null,"rationale":"x"}`},
			rejected: []string{
				`{"verdict":"SWAP","revised_arguments":null,"rationale":"x"}`,
				`{"verdict":"APPROVE","revised_arguments":null,"swap_to_cluster":null,"rationale":"x"}`,
			},
		},
		{
			name:     "consensus vote",
			schema:   ConsensusVoteJSONSchema,
			accept:   []string{`{"decision":"approve","reason":"x"}`},
			rejected: []string{`{"decision":"approve"}`, `{"decision":"maybe","reason":"x"}`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			compiled := compileCanonicalSchema(t, tt.schema)
			validator := jsonschema.NewValidator()
			for _, answer := range tt.accept {
				assert.Empty(t, validator.Validate(compiled, []byte(answer)), answer)
			}
			for _, answer := range tt.rejected {
				assert.NotEmpty(t, validator.Validate(compiled, []byte(answer)), answer)
			}
		})
	}
}

// TestResponseSchemas_UseOnlyThePortableSubset: a Tribunal seat or L2 vote may
// be served by any registry model, local or hosted, so every schema is one
// every provider applies, and the same schema constrains every model.
func TestResponseSchemas_UseOnlyThePortableSubset(t *testing.T) {
	for name, schema := range map[string]string{
		"generation":                   generationJSONSchema,
		"marshal":                      marshalJSONSchema,
		"auditor with alternatives":    auditorJSONSchema([]string{"cluster_b", "cluster_c"}),
		"auditor without alternatives": auditorJSONSchema(nil),
		"consensus vote":               ConsensusVoteJSONSchema,
	} {
		require.NoError(t, models.ValidatePortableResponseSchema(schema), name)
	}
}

// TestDecodersRejectTheLiveUnconstrainedShapes pins the answers that ended
// every live shell proposal before the schemas existed, and the shapes the
// portable schemas cannot exclude: decoding stays strict whether or not a
// provider honours the schema.
func TestDecodersRejectTheLiveUnconstrainedShapes(t *testing.T) {
	t.Run("marshal non-JSON fails closed HIGH", func(t *testing.T) {
		inferer := &fakeInferer{marshal: "Risk: LOW. The command only reads state."}
		risk, err := assessRisk(context.Background(), testConfig(inferer), Request{Request: testRequest}, argsFor("ss -tlpn"))
		require.NoError(t, err)
		assert.True(t, risk.Invalid)
		assert.Equal(t, RiskHigh, risk.Level)
	})
	t.Run("empty proposal abstains", func(t *testing.T) {
		// The portable schema admits an empty command; the decoder's
		// executable-name check still rejects it, retries with feedback, and
		// the exhausted member abstains.
		inferer := &fakeInferer{generate: func(_ uint32, member Member) string {
			if member == MemberAxiom {
				return `{"command":"","args":[]}`
			}
			return `{"command":"ss","args":["-l"]}`
		}}
		candidates, err := generate(testutil.Context(t), testConfig(inferer), Request{Request: testRequest}, 1, nil)
		require.NoError(t, err)
		for _, candidate := range candidates {
			if candidate.Member == MemberAxiom {
				assert.Empty(t, candidate.Arguments.Command, "an empty proposal joins no cluster")
				continue
			}
			assert.Equal(t, "ss", candidate.Arguments.Command)
		}
		var retried bool
		for _, call := range inferer.calls {
			if call.Persona == string(MemberAxiom) && strings.Contains(call.User, "command must be one executable name") {
				retried = true
			}
		}
		assert.True(t, retried, "the empty command is fed back as invalid")
	})
	split := vote(1, candidatesFor(map[Member]string{
		MemberAxiom: "ss -tlpn", MemberConcord: "ss -tlpn", MemberVariance: "ss -tlpn", MemberPragma: "netstat -tlpn", MemberNemesis: "lsof -i",
	}))
	for name, answer := range map[string]string{
		"auditor revision with timeout":  `{"verdict":"REVISE","revised_arguments":{"command":"ss","args":["-tlpn"],"timeout":30},"swap_to_cluster":null,"rationale":"x"}`,
		"auditor approve with revision":  `{"verdict":"APPROVE","revised_arguments":{"command":"ss","args":["-tlpn"]},"swap_to_cluster":null,"rationale":"x"}`,
		"auditor approve with swap":      `{"verdict":"APPROVE","revised_arguments":null,"swap_to_cluster":"cluster_b","rationale":"x"}`,
		"auditor revise with null":       `{"verdict":"REVISE","revised_arguments":null,"swap_to_cluster":null,"rationale":"x"}`,
		"auditor revise without command": `{"verdict":"REVISE","revised_arguments":{"command":"","args":[]},"swap_to_cluster":null,"rationale":"x"}`,
		"auditor revise with swap":       `{"verdict":"REVISE","revised_arguments":{"command":"ss","args":["-l"]},"swap_to_cluster":"cluster_b","rationale":"x"}`,
		"auditor swap with revision":     `{"verdict":"SWAP","revised_arguments":{"command":"ss","args":[]},"swap_to_cluster":"cluster_b","rationale":"x"}`,
		"auditor swap without cluster":   `{"verdict":"SWAP","revised_arguments":null,"swap_to_cluster":null,"rationale":"x"}`,
	} {
		t.Run(name, func(t *testing.T) {
			inferer := &fakeInferer{audit: []string{answer}}
			result, err := audit(context.Background(), testConfig(inferer), Request{Request: testRequest}, split)
			require.NoError(t, err)
			assert.NotEmpty(t, result.Failure)
			assert.Equal(t, evalv1.AgentReviewVerdict_AGENT_REVIEW_VERDICT_UNSPECIFIED, result.Verdict)
		})
	}
}

func TestEveryRoleCallCarriesItsSchema(t *testing.T) {
	inferer := &fakeInferer{
		generate: proposals(map[Member]string{MemberAxiom: "ss -l", MemberConcord: "ss -l", MemberVariance: "ss -l", MemberPragma: "ss -l", MemberNemesis: "ss -l"}),
		marshal:  lowRisk,
		audit:    []string{approve},
	}
	_, err := Run(context.Background(), testConfig(inferer), Request{Request: testRequest}, 0)
	require.NoError(t, err)
	require.Len(t, inferer.calls, len(AllMembers)+2)
	for _, call := range inferer.calls {
		switch call.Stage {
		case StageGenerate:
			assert.Equal(t, generationJSONSchema, call.JSONSchema) //nolint:testifylint // exact bytes, not JSON equivalence
		case StageMarshal:
			assert.Equal(t, marshalJSONSchema, call.JSONSchema) //nolint:testifylint // exact bytes, not JSON equivalence
		case StageAudit:
			// A unanimous panel leaves the Auditor no SWAP shape.
			assert.Equal(t, auditorJSONSchema(nil), call.JSONSchema)
			assert.NotContains(t, call.User, "SWAP are allowed")
		}
	}
}

func TestRegenerationShowsOnlyAuthoredArguments(t *testing.T) {
	prior := []Cluster{{ID: "cluster_a", Arguments: argsFor("ss -l"), Members: []Member{MemberAxiom}}}
	prior[0].Arguments.Timeout, prior[0].Arguments.WorkingDir = 23, "/srv/work"
	for _, member := range AllMembers {
		prompt := generationPrompt(Request{Request: testRequest}, member, prior)
		assert.Contains(t, prompt, `{"command":"ss","args":["-l"]}`)
		assert.NotContains(t, prompt, "timeout")
		assert.NotContains(t, prompt, "working_dir")
	}
}

// TestRoundTwoGivesNemesisItsAdversarialLens keeps Nemesis attacking the
// emerging consensus in round 2 while the honest members converge on it.
func TestRoundTwoGivesNemesisItsAdversarialLens(t *testing.T) {
	prior := []Cluster{{ID: "cluster_a", Arguments: argsFor("ss -l"), Members: []Member{MemberAxiom, MemberConcord}}}
	for _, member := range AllMembers {
		prompt := generationPrompt(Request{Request: testRequest}, member, prior)
		if member == MemberNemesis {
			assert.Contains(t, prompt, "only adversarial seat")
			assert.Contains(t, prompt, "real semantic flaw")
			assert.NotContains(t, prompt, "you may adopt a proposal or improve it")
			continue
		}
		assert.Contains(t, prompt, "you may adopt a proposal or improve it")
		assert.NotContains(t, prompt, "adversarial")
	}
	assert.Equal(t, generationPrompt(Request{Request: testRequest}, MemberNemesis, nil), generationPrompt(Request{Request: testRequest}, MemberAxiom, nil),
		"round 1 gives every seat the same request")
}

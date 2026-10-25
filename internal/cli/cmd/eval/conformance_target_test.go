// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package eval

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func conformanceSelectionCommand(input string, jsonOutput bool) (*cobra.Command, *bytes.Buffer) {
	cmd := &cobra.Command{Use: "selection"}
	cmd.SetContext(context.Background())
	cmd.PersistentFlags().Bool("json", jsonOutput, "")
	cmd.SetIn(strings.NewReader(input))
	out := new(bytes.Buffer)
	cmd.SetErr(out)
	return cmd, out
}

func conformanceSelectionOperators() []*operatorv1.OperatorDocument {
	embedded := &operatorv1.OperatorDocument{
		Id: string(constants.DocIDEmbeddedOperator), OperatorSessionId: conformanceTestEmbeddedSession,
		OperatorType: string(constants.OperatorTypeEmbedded), Status: string(constants.OperatorStatusActive),
		RuntimeConfig: &operatorv1.OperatorRuntimeConfig{},
	}
	alpha := conformanceTestOutboundDocument()
	alpha.Name, alpha.CurrentHostname = "Alpha", "workstation"
	beta := conformanceTestOutboundDocument()
	beta.Id, beta.OperatorSessionId, beta.Name = "beta", "beta-session", "Beta"
	inference := conformanceTestOutboundDocument()
	inference.Id = "inference"
	inference.OperatorCapabilities = []string{string(constants.OperatorCapabilityInference)}
	inactive := conformanceTestOutboundDocument()
	inactive.Id, inactive.Status = "offline", string(constants.OperatorStatusStopped)
	return []*operatorv1.OperatorDocument{nil, beta, inactive, embedded, inference, alpha}
}

func TestConformanceDataOperatorSelection_PromptsForEveryActiveDataOperatorByName(t *testing.T) {
	cmd, out := conformanceSelectionCommand("bad\n0\n4\n2\n", false)
	selected, err := selectConformanceDataOperator(cmd, conformanceSelectionOperators(), "")
	require.NoError(t, err)
	assert.Equal(t, "beta", selected.GetId())
	assert.Contains(t, out.String(), "1. Alpha on workstation")
	assert.Contains(t, out.String(), "2. Beta")
	assert.Contains(t, out.String(), "3. Gateway (embedded Data Operator)")
	assert.NotContains(t, out.String(), "offline")
	assert.NotContains(t, out.String(), "inference")
	assert.Equal(t, 3, strings.Count(out.String(), "Enter a number"))
}

func TestConformanceDataOperatorSelection_AutomaticAndExplicitTargets(t *testing.T) {
	for _, op := range conformanceSelectionOperators()[3:] {
		if op.GetId() == "inference" {
			continue
		}
		cmd, out := conformanceSelectionCommand("", false)
		selected, err := selectConformanceDataOperator(cmd, []*operatorv1.OperatorDocument{op}, "")
		require.NoError(t, err)
		assert.Same(t, op, selected)
		assert.Empty(t, out.String())
	}
	cmd, out := conformanceSelectionCommand("", true)
	selected, err := selectConformanceDataOperator(cmd, conformanceSelectionOperators(), "beta")
	require.NoError(t, err)
	assert.Equal(t, "beta", selected.GetId())
	assert.Empty(t, out.String())
}

func TestConformanceDataOperatorSelection_FailsWithoutGuessing(t *testing.T) {
	tests := []struct {
		name  string
		id    string
		input string
		json  bool
		want  error
	}{
		{name: "end of input", want: constants.ErrEvaluationTargetAmbiguous},
		{name: "json does not prompt", input: "1\n", json: true, want: constants.ErrEvaluationTargetAmbiguous},
		{name: "inactive target", id: "offline", want: constants.ErrEvaluationTargetUnavailable},
		{name: "inference only", id: "inference", want: constants.ErrEvaluationTargetUnavailable},
		{name: "unknown target", id: "missing", want: constants.ErrEvaluationTargetUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, out := conformanceSelectionCommand(tt.input, tt.json)
			_, err := selectConformanceDataOperator(cmd, conformanceSelectionOperators(), tt.id)
			require.ErrorIs(t, err, tt.want)
			if tt.json {
				assert.Empty(t, out.String())
			}
		})
	}
	cmd, _ := conformanceSelectionCommand("1\n", false)
	op := conformanceTestOutboundDocument()
	other := conformanceTestOutboundDocument()
	other.OperatorSessionId = "another-session"
	_, err := selectConformanceDataOperator(cmd, []*operatorv1.OperatorDocument{op, other}, op.Id)
	require.ErrorIs(t, err, constants.ErrEvaluationTargetAmbiguous)
}

func TestConformanceOperatorRoot_UsesEnrolledRuntimeAndExplicitOverride(t *testing.T) {
	enrolledRoot, err := filepath.Abs("operator")
	require.NoError(t, err)
	mountedRoot, err := filepath.Abs("mounted-operator")
	require.NoError(t, err)
	op := conformanceTestOutboundDocument()
	op.LocalDir = enrolledRoot
	op.RuntimeConfig = &operatorv1.OperatorRuntimeConfig{LocalDir: "/different-workspace"}
	root, err := conformanceOperatorRoot("", op)
	require.NoError(t, err)
	assert.Equal(t, enrolledRoot, root)
	root, err = conformanceOperatorRoot(mountedRoot, op)
	require.NoError(t, err)
	assert.Equal(t, mountedRoot, root)
	op.LocalDir, op.RuntimeConfig = "", nil
	_, err = conformanceOperatorRoot("", op)
	require.ErrorIs(t, err, constants.ErrEvaluationTargetUnavailable)
}

func TestConformanceConsensusAnchorsPinsSupportingEnrollmentKeys(t *testing.T) {
	key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)).Public().(ed25519.PublicKey)
	keyHex := hex.EncodeToString(key)
	supporting := conformanceTestOutboundDocument()
	supporting.Id = "inference-observer"
	supporting.OperatorCapabilities = []string{string(constants.OperatorCapabilityInference)}
	supporting.ActuatorKeyId, supporting.ActuatorPublicKey = keyHex, keyHex
	executor := conformanceTestOutboundDocument()
	embedded := &operatorv1.OperatorDocument{Id: string(constants.DocIDEmbeddedOperator), Status: string(constants.OperatorStatusActive)}
	inactive := &operatorv1.OperatorDocument{Id: "offline", Status: string(constants.OperatorStatusStopped), ActuatorKeyId: "invalid"}
	unbound := &operatorv1.OperatorDocument{Id: "unbound", Status: string(constants.OperatorStatusActive)}
	anchors, err := conformanceConsensusAnchors([]*operatorv1.OperatorDocument{nil, executor, embedded, inactive, unbound, supporting}, executor.Id)
	require.NoError(t, err)
	require.Len(t, anchors, 1)
	assert.Equal(t, supporting.Id, anchors[0].GetRuntimeId())
	assert.Equal(t, keyHex, anchors[0].GetKeyId())
	assert.Equal(t, keyHex, anchors[0].GetPublicKeyHex())
	assert.Empty(t, anchors[0].GetExportSha256())
}

func TestConformanceConsensusAnchorsRejectsInvalidEnrollmentKeys(t *testing.T) {
	for _, fields := range [][2]string{{"invalid", ""}, {"", "invalid"}, {"invalid", "invalid"}} {
		doc := conformanceTestOutboundDocument()
		doc.Id = "supporting"
		doc.ActuatorKeyId, doc.ActuatorPublicKey = fields[0], fields[1]
		_, err := conformanceConsensusAnchors([]*operatorv1.OperatorDocument{doc}, conformanceTestOperatorID)
		require.ErrorIs(t, err, constants.ErrConformanceAnchorInvalid)
	}
}

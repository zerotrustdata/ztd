// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.
//go:build integration

package drive

import (
	"context"
	"crypto/tls"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/operator"
	"github.com/zerotrustdata/ztd/internal/config"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/services/network"
	"github.com/zerotrustdata/ztd/internal/testutil"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	agentconfig "github.com/zerotrustdata/ztd/internal/tools/agent_harness/config"
	"github.com/zerotrustdata/ztd/internal/uuid"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

// integrationRows are the DIRECT doctrine rows the Tier 2 test drives, plus the
// first row-13 corpus entry.
var integrationRows = map[int]bool{1: true, 3: true, 4: true, 6: true, 8: true, 10: true, 11: true, 13: true}

// directFixture is one live Gateway with its CLI identity and a DIRECT driver.
type directFixture struct {
	f       *fixtures.GatewayFixture
	direct  *Direct
	ident   *fixtures.ClientIdentity
	workDir string
}

// newDirectFixture boots a Gateway under posture and builds a DIRECT driver
// that submits as the bootstrapped CLI identity to the embedded Operator.
func newDirectFixture(t *testing.T, posture config.GatewayPosture) *directFixture {
	t.Helper()
	f := fixtures.NewGatewayFixture(t, fixtures.GatewayFixtureOptions{
		TestName: "conformance-direct-" + string(posture),
		Posture:  posture,
	})
	f.WaitForReady(t)
	ident := fixtures.BootstrapCLIIdentity(t, f, "conformance-direct-host")

	cliCert, err := tls.X509KeyPair([]byte(ident.CLICertificate), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: ident.CLIPrivateKey}))
	require.NoError(t, err)
	caBundle := append(append([]byte{}, testutil.ReadRootCA(t, f.PKIDir)...), testutil.ReadOperatorCA(t, f.PKIDir)...)
	caPath := filepath.Join(testutil.TempDir(t), "ca.pem")
	require.NoError(t, os.WriteFile(caPath, caBundle, 0o600))

	url := network.LocalhostHTTPSURL(f.Service.GetHTTPSPort())
	c, err := client.New(agentconfig.Config{
		MTLSBaseURL:   url,
		PublicBaseURL: url,
		Auth:          agentconfig.Auth{Certificate: &cliCert, CABundle: caPath},
	})
	require.NoError(t, err)

	persona := client.Persona{ID: "conformance-direct", UserID: ident.UserID, CLISessionID: ident.CLISessionID}
	target := operator.Target{OperatorID: ident.OperatorID, OperatorSessionID: ident.OperatorSessionID}
	direct, err := NewDirect(c, persona, target, nil, BaseMutators())
	require.NoError(t, err)

	// The embedded Operator confines file edits to its working directory, so the
	// case targets live under the fixture's test-results directory beneath it.
	return &directFixture{f: f, direct: direct, ident: ident, workDir: f.Config.WorkDir}
}

// attemptFor builds a fresh attempt for one case, with targets under the
// fixture work directory and removed when the test ends.
func (fx *directFixture) attemptFor(t *testing.T, c cases.Case) cases.Attempt {
	t.Helper()
	attemptID, err := uuid.NewString()
	require.NoError(t, err)
	runID, err := uuid.NewString()
	require.NoError(t, err)
	dir := filepath.Join(fx.workDir, "test-results", "conformance-direct", runID)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return cases.Attempt{
		RunID:     runID,
		AttemptID: attemptID,
		Case:      c,
		TargetAbs: filepath.Join(dir, "target.txt"),
		ReadAbs:   filepath.Join(dir, "read.txt"),
		Marker:    cases.Marker(runID, attemptID),
	}
}

// answerText renders the human text of an answer for assertion messages.
func answerText(got *evalv1.ConformanceObservation) string {
	switch {
	case got.GetRefusal() != nil:
		return got.GetRefusal().GetCode().String() + ": " + got.GetRefusal().GetError()
	case got.GetRejection() != nil:
		return got.GetRejection().GetReceipt().GetRejectionCode().String() + ": " + got.GetRejection().GetError()
	default:
		return got.GetKind().String()
	}
}

// TestDirectDriveMatchesCatalogueOnDoctrineRows drives the selected DIRECT rows
// against a real doctrine Gateway and asserts the observation kind and code
// that the catalogue expects.
func TestDirectDriveMatchesCatalogueOnDoctrineRows(t *testing.T) {
	all, _, err := cases.Catalogue()
	require.NoError(t, err)

	fx := newDirectFixture(t, config.PostureDoctrine)
	drove13 := false
	for _, c := range all {
		if c.Ingress != cases.IngressDirect || c.Posture != evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE {
			continue
		}
		if !integrationRows[c.Row] || (c.Row == 13 && drove13) {
			continue
		}
		if c.Row == 13 {
			drove13 = true
		}
		t.Run(c.ID, func(t *testing.T) {
			driven, err := fx.direct.Drive(context.Background(), fx.attemptFor(t, c))
			require.NoError(t, err)
			require.Len(t, driven.Observations, len(c.Expect))
			for i, want := range c.Expect {
				assertObservation(t, want, driven.Observations[i])
			}
		})
	}
	require.True(t, drove13, "catalogue has no row-13 DIRECT doctrine case")
}

// assertObservation checks one observation against one catalogue expectation.
func assertObservation(t *testing.T, want cases.Expect, got *evalv1.ConformanceObservation) {
	t.Helper()
	switch want.Kind {
	case cases.ExpectExecuted:
		assert.Equal(t, evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT, got.GetKind(), "answer: %s", answerText(got))
		assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, got.GetReceipt().GetStatus(), "answer: %s", answerText(got))
	case cases.ExpectRejected:
		assert.Equal(t, evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REJECTION, got.GetKind())
		assert.Equal(t, want.RejectionCode, got.GetRejection().GetReceipt().GetRejectionCode())
	case cases.ExpectRefused:
		assert.Equal(t, evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REFUSAL, got.GetKind())
		assert.Equal(t, want.RefusalCode, got.GetRefusal().GetCode())
	default:
		t.Fatalf("unexpected expectation kind %q", want.Kind)
	}
}

// TestDirectProbeWrongTargetByPosture records the observation for the row 9
// fault (wrong operator session) under each posture. It pins nothing: the
// tracker pins row 9 from this output.
func TestDirectProbeWrongTargetByPosture(t *testing.T) {
	for _, posture := range []config.GatewayPosture{config.PostureDoctrine, config.PostureConsensus, config.PostureRatify, config.PostureNotary} {
		t.Run(string(posture), func(t *testing.T) {
			fx := newDirectFixture(t, posture)
			c := cases.Case{ID: "row-9-probe-" + string(posture), Row: 9, Ingress: cases.IngressDirect, Fault: cases.FaultWrongTarget, Action: cases.ActionMutation}
			driven, err := fx.direct.Drive(context.Background(), fx.attemptFor(t, c))
			require.NoError(t, err)
			require.Len(t, driven.Observations, 1)
			obs := driven.Observations[0]
			t.Logf("Row 9 probe posture=%s kind=%s http=%d refusal=%s rejection=%s",
				posture, obs.GetKind(), obs.GetHttpStatus(), obs.GetRefusal().GetCode(), obs.GetRejection().GetReceipt().GetRejectionCode())
			assert.NotEqual(t, evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_UNTYPED, obs.GetKind())
		})
	}
}

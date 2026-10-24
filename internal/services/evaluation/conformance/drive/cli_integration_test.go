// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package drive

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/api"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/cli/operator"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/network"
	"github.com/zerotrustdata/ztd/internal/uuid"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

// cliPosterTimeout bounds one CLI dispatch in these tests. Doctrine answers at once;
// the attended ratify path holds the request until the test approves it.
const cliPosterTimeout = 60 * time.Second

// installCLIRuntime materializes the identity's CLI runtime (certificate, key, trust
// bundle, saved credentials) and returns the file service and config the production
// CLI reads it from.
func installCLIRuntime(t *testing.T, f *fixtures.GatewayFixture, ident *fixtures.ClientIdentity) (fs.RuntimeFileService, *config.Config) {
	t.Helper()
	fileSvc, cliCfg := cmdtest.NewCmdTestEnv(t)
	ident.InstallCLIRuntime(t, f, fileSvc, cliCfg)
	return fileSvc, cliCfg
}

// cliPosterFor returns the Gateway API client that `ztd operator run` and the
// conformance command post through, built from the CLI runtime on disk.
func cliPosterFor(t *testing.T, f *fixtures.GatewayFixture, ident *fixtures.ClientIdentity) *api.Client {
	t.Helper()
	fileSvc, cliCfg := installCLIRuntime(t, f, ident)
	poster, err := api.NewClientWithOptions(fileSvc, cliCfg, api.ClientOptions{Timeout: cliPosterTimeout})
	require.NoError(t, err)
	return poster
}

// gatewayAttempt builds a fresh attempt for c. Its target and read files sit under the
// Gateway's work directory, where the embedded Operator edits files, and are removed
// when the test ends.
func gatewayAttempt(t *testing.T, f *fixtures.GatewayFixture, c cases.Case) cases.Attempt {
	t.Helper()
	attemptID, err := uuid.NewString()
	require.NoError(t, err)
	runID, err := uuid.NewString()
	require.NoError(t, err)
	results := filepath.Join(f.Config.WorkDir, "test-results")
	parent := filepath.Join(results, "conformance-drive")
	dir := filepath.Join(parent, runID)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
		// Drop the parents once empty so no run leaves directories in the source tree.
		_ = os.Remove(parent)
		_ = os.Remove(results)
	})
	return cases.Attempt{
		RunID:     runID,
		AttemptID: attemptID,
		Case:      c,
		TargetAbs: filepath.Join(dir, "target.txt"),
		ReadAbs:   filepath.Join(dir, "read.txt"),
		Marker:    cases.Marker(runID, attemptID),
	}
}

// assertExpect checks one observation against one catalogue expectation.
func assertExpect(t *testing.T, want cases.Expect, got *evalv1.ConformanceObservation) {
	t.Helper()
	switch want.Kind {
	case cases.ExpectExecuted:
		assert.Equal(t, evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT, got.GetKind())
		assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, got.GetReceipt().GetStatus())
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

// TestCLIDriveMatchesRealDoctrineGateway drives the CLI rows the catalogue expects under
// doctrine against a live Gateway. Row 1 executes the marker write, and each row 14 case
// is refused by L1 before any envelope exists.
func TestCLIDriveMatchesRealDoctrineGateway(t *testing.T) {
	all, _, err := cases.Catalogue()
	require.NoError(t, err)
	doctrine := evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE

	f := fixtures.NewGatewayFixture(t, fixtures.GatewayFixtureOptions{TestName: "conformance-cli-doctrine", Posture: constants.PostureDoctrine})
	f.WaitForReady(t)
	ident := fixtures.BootstrapCLIIdentity(t, f, "conformance-cli-host")
	target := operator.Target{OperatorID: ident.OperatorID, OperatorSessionID: ident.OperatorSessionID}
	cli, err := NewCLI(cliPosterFor(t, f, ident), target, ident.CLISessionID)
	require.NoError(t, err)

	var selected []cases.Case
	for _, c := range all {
		if c.Ingress == cases.IngressCLI && c.Posture == doctrine && (c.Row == 1 || c.Row == 14) {
			selected = append(selected, c)
		}
	}
	require.NotEmpty(t, selected, "catalogue has no row 1 or row 14 CLI doctrine case")
	for _, c := range selected {
		t.Run(c.ID, func(t *testing.T) {
			a := gatewayAttempt(t, f, c)
			driven, err := cli.Drive(t.Context(), a)
			require.NoError(t, err)
			require.Len(t, driven.Observations, len(c.Expect))
			obs := driven.Observations[0]
			assertExpect(t, c.Expect[0], obs)
			assert.Equal(t, int32(0), obs.GetHttpStatus(), "the CLI answer has no HTTP status")

			if c.Expect[0].Kind == cases.ExpectExecuted {
				written, err := os.ReadFile(a.TargetAbs)
				require.NoError(t, err)
				assert.Equal(t, a.Marker, string(written), "the executed command must write exactly the marker")
				return
			}
			_, statErr := os.Stat(a.TargetAbs)
			assert.True(t, os.IsNotExist(statErr), "a refused command must leave no file")
		})
	}
}

// TestCLIDriveUnreachableGatewayAbortsTheAttempt points the production Gateway API client
// at a loopback port nothing listens on. The attempt aborts with the connection error and
// records no observation, because an unreachable Gateway is not an UNTYPED answer.
func TestCLIDriveUnreachableGatewayAbortsTheAttempt(t *testing.T) {
	f := fixtures.NewGatewayFixture(t, fixtures.GatewayFixtureOptions{TestName: "conformance-cli-unreachable", Posture: constants.PostureDoctrine})
	f.WaitForReady(t)
	ident := fixtures.BootstrapCLIIdentity(t, f, "conformance-cli-host")
	fileSvc, cliCfg := installCLIRuntime(t, f, ident)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	poster, err := api.NewClientWithURL(fileSvc, cliCfg, network.LocalhostHTTPSURL(port))
	require.NoError(t, err)

	target := operator.Target{OperatorID: ident.OperatorID, OperatorSessionID: ident.OperatorSessionID}
	cli, err := NewCLI(poster, target, ident.CLISessionID)
	require.NoError(t, err)
	c := cases.Case{ID: "cli-unreachable", Row: 1, Ingress: cases.IngressCLI, Fault: cases.FaultNone, Action: cases.ActionMutation,
		Posture: evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE}
	a := gatewayAttempt(t, f, c)

	driven, err := cli.Drive(t.Context(), a)
	require.Error(t, err)
	// Portable connection-refused check: Unix reports ECONNREFUSED
	// ("connection refused") while Windows reports WSAECONNREFUSED
	// ("actively refused"). Accept either the errno or the message.
	if !errors.Is(err, syscall.ECONNREFUSED) {
		assert.Contains(t, strings.ToLower(err.Error()), "refus",
			"unreachable gateway must fail with connection refused, got: %v", err)
	}
	assert.Empty(t, driven.Observations)
	_, statErr := os.Stat(a.TargetAbs)
	assert.True(t, os.IsNotExist(statErr), "an unreachable Gateway must write nothing")
}

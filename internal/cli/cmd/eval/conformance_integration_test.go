// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package eval

import (
	"context"
	"crypto/ed25519"
	"log/slog"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/drive"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/run"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
	"github.com/zerotrustdata/ztd/internal/testutil"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

// newConformanceStackRig is the conformance command over the real eval stack. The
// Gateway, its embedded Operator, the CLI identity, and the Data and Inference
// Operators are production; the model provider is the only stub.
//
// The CLI's project root is the Gateway's own runtime root, as it is on the Gateway
// host. The embedded executor runs and reads its canaries under that root, and its
// anchor is the Gateway's actuator export, which gw start writes there. The Data
// Operator's root is the rig's Operator root. The run ID is fixed so verdicts can
// name it.
func newConformanceStackRig(t *testing.T) (conformanceRig, *realStackRun) {
	t.Helper()
	env := newRealStackEnvironment(t, fixtures.EvalStackOptions{Posture: constants.PostureDoctrine}, defaultRunControlDeps())
	data, err := env.stack.Data.Workload()
	require.NoError(t, err)

	// The project root is the parent of the Gateway's runtime directory.
	gatewayRoot := filepath.Dir(env.stack.Gateway.FileService.Resolve(""))
	gatewayFiles, err := fs.NewRuntimeFileService(gatewayRoot, testutil.NewTestLogger())
	require.NoError(t, err)
	gatewayCfg, err := env.deps.configLoader(gatewayRoot)
	require.NoError(t, err)
	env.stack.Identity.InstallCLIRuntime(t, env.stack.Gateway, gatewayFiles, gatewayCfg)
	// gw start exports the Gateway's actuator public key into its PKI dir; the fixture
	// does not start the Gateway through gw start, so the rig runs the same export with
	// the Gateway's own actuator key.
	actuatorPub, ok := env.stack.Gateway.ActuatorPriv.Public().(ed25519.PublicKey)
	require.True(t, ok, "the Gateway actuator key is ed25519")
	require.NoError(t, governance.ExportActuatorPublicKey(gatewayFiles, actuatorPub, env.stack.Gateway.ActuatorKeyID, nil))

	deps := env.deps
	deps.fileSvcFactory = conformanceRootFactory(gatewayRoot, env.stack.Gateway.Keystore, env.stack.DataHost.Keyring)
	deps.newID = func() (string, error) { return conformanceTestRunID, nil }
	rig := conformanceRig{gwRoot: gatewayRoot, opRoot: env.stack.DataHost.WorkingDir, operatorID: data.OperatorID, deps: deps}
	return rig, env
}

// conformanceRootFactory opens each runtime the conformance command reads with the
// vault that runtime seals under: the Gateway's root with the Gateway's own key
// store, and every other root, the Data Operator's, with that Operator's key store.
func conformanceRootFactory(gatewayRoot string, gatewayKeys *keystore.Keystore, operatorKeyring keystore.Keyring) func(string, *slog.Logger) (fs.RuntimeFileService, error) {
	operatorFactory := shared.EvidenceFileSvcFactory(keystore.Options{Keyring: operatorKeyring})
	return func(root string, logger *slog.Logger) (fs.RuntimeFileService, error) {
		if root != gatewayRoot {
			return operatorFactory(root, logger)
		}
		files, err := fs.NewRuntimeFileService(root, logger)
		if err != nil {
			return nil, err
		}
		return evaluation.NewEvidenceFileService(files, func(fs.RuntimeFileService) (evaluation.EvidenceVault, error) {
			return gatewayKeys.OpenVault()
		})
	}
}

// capturingRun records the config the suite runs with, then runs the production
// suite against the real stack.
func capturingRun(captured *run.Config) func(context.Context, run.Config) (*evalv1.ConformanceRunRecord, error) {
	return func(ctx context.Context, cfg run.Config) (*evalv1.ConformanceRunRecord, error) {
		*captured = cfg
		return run.Run(ctx, cfg)
	}
}

// conformanceMenuSession returns the session the Data Operator menu lists as choice n.
func conformanceMenuSession(t *testing.T, out string, n int) string {
	t.Helper()
	menu := regexp.MustCompile(`(?m)^\s+(\d+)\. .* \(session ([^)]+)\)$`)
	for _, match := range menu.FindAllStringSubmatch(out, -1) {
		if match[1] == strconv.Itoa(n) {
			return match[2]
		}
	}
	t.Fatalf("Data Operator menu has no choice %d in:\n%s", n, out)
	return ""
}

func TestConformanceRun_WithoutFlagsOnRealStackRequiresAChoice(t *testing.T) {
	rig, _ := newConformanceStackRig(t)
	// The Gateway's embedded Operator and the outbound Data Operator are both active,
	// so a run without --operator-id has no single target to pick.
	rig.deps.conformance.run = func(context.Context, run.Config) (*evalv1.ConformanceRunRecord, error) {
		t.Fatal("run called before a Data Operator was chosen")
		return nil, nil
	}

	out, err := rig.execute(t, "conformance", "run")
	assert.ErrorIs(t, err, constants.ErrEvaluationTargetAmbiguous)
	assert.Contains(t, out, "Choose a Data Operator")
}

func TestConformanceRun_EmbeddedDataOperatorByIDRunsGatewayLocalCases(t *testing.T) {
	rig, _ := newConformanceStackRig(t)
	var captured run.Config
	rig.deps.conformance.run = capturingRun(&captured)

	out, err := rig.execute(t, "conformance", "run", "--operator-id", string(constants.DocIDEmbeddedOperator))
	require.NotErrorIs(t, err, constants.ErrEvaluationTargetAmbiguous)
	assert.Contains(t, out, "Suite: ", "the suite ran to a verdict")
	require.Len(t, captured.Roots, 1)
	assert.Equal(t, oracle.GatewayRuntimeID, captured.Roots[cases.ExecutorEmbedded].RuntimeID)
	require.Len(t, captured.Drivers, 3)
	require.Len(t, captured.Unavailable, 3)
	assert.Equal(t, cases.IngressCLI.UnavailableObservation(), captured.Unavailable[cases.IngressCLI])
}

func TestConformanceRun_PromptsForTheDataOperatorAndRunsTheChosenOne(t *testing.T) {
	rig, env := newConformanceStackRig(t)
	data, err := env.stack.Data.Workload()
	require.NoError(t, err)
	var captured run.Config
	rig.deps.conformance.run = capturingRun(&captured)

	// Choice 1 is whichever Data Operator the menu lists first, so the assertions
	// follow the menu rather than an assumed order.
	out, err := rig.executeInput(t, "1\n", "conformance", "run")
	require.NotErrorIs(t, err, constants.ErrEvaluationTargetAmbiguous)
	assert.Contains(t, out, "Choose a Data Operator")
	assert.Contains(t, out, "Suite: ", "the suite ran to a verdict")
	if conformanceMenuSession(t, out, 1) == data.OperatorSessionID {
		require.Len(t, captured.Roots, 2)
		assert.Equal(t, rig.operatorID, captured.Roots[cases.ExecutorOutbound].RuntimeID)
		require.Len(t, captured.Drivers, 5)
		require.Len(t, captured.Unavailable, 1)
		return
	}
	require.Len(t, captured.Roots, 1)
	assert.Equal(t, oracle.GatewayRuntimeID, captured.Roots[cases.ExecutorEmbedded].RuntimeID)
	require.Len(t, captured.Drivers, 3)
}

func TestConformanceRun_PassesConfiguredDriversAndRootsToRun(t *testing.T) {
	rig, _ := newConformanceStackRig(t)
	var captured run.Config
	rig.deps.conformance.run = capturingRun(&captured)

	out, err := rig.execute(t, rig.runArgs()...)
	require.NotErrorIs(t, err, constants.ErrEvaluationTargetAmbiguous)
	assert.Contains(t, out, "Suite: ", "the suite ran to a verdict")

	require.Len(t, captured.Drivers, 6, "one driver per ingress")
	assert.IsType(t, &drive.Direct{}, captured.Drivers[cases.IngressDirect])
	assert.IsType(t, &drive.Dispatch{}, captured.Drivers[cases.IngressDispatchEmbedded])
	assert.IsType(t, &drive.Dispatch{}, captured.Drivers[cases.IngressDispatchOutbound])
	assert.IsType(t, &drive.CLI{}, captured.Drivers[cases.IngressCLI])
	assert.IsType(t, &drive.MCP{}, captured.Drivers[cases.IngressMCP], "unattended MCP is not wrapped")
	assert.IsType(t, &drive.A2A{}, captured.Drivers[cases.IngressA2A], "unattended A2A is not wrapped")
	assert.NotNil(t, captured.Sentinel)
	assert.False(t, captured.Attended)

	posture, err := captured.Posture(context.Background())
	require.NoError(t, err)
	assert.Equal(t, evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE, posture)

	gwRuntime, err := fs.NewRuntimeFileService(rig.gwRoot, testutil.NewTestLogger())
	require.NoError(t, err)
	opRuntime, err := fs.NewRuntimeFileService(rig.opRoot, testutil.NewTestLogger())
	require.NoError(t, err)
	require.Len(t, captured.Roots, 2)
	assert.Equal(t, gwRuntime.Resolve(""), captured.Roots[cases.ExecutorEmbedded].Dir, "Dir is the executor's runtime directory, which canary paths and the FILE_EDIT target policy resolve against")
	assert.Equal(t, oracle.GatewayRuntimeID, captured.Roots[cases.ExecutorEmbedded].RuntimeID)
	assert.Nil(t, captured.Roots[cases.ExecutorEmbedded].Bound)
	assert.Equal(t, opRuntime.Resolve(""), captured.Roots[cases.ExecutorOutbound].Dir)
	assert.Equal(t, rig.operatorID, captured.Roots[cases.ExecutorOutbound].RuntimeID)
	bound := captured.Roots[cases.ExecutorOutbound].Bound
	require.NotNil(t, bound, "the outbound anchor is the key bound at enrollment")
	assert.Equal(t, rig.operatorID, bound.GetRuntimeId())
	assert.NotEmpty(t, bound.GetPublicKeyHex())
	assert.Empty(t, bound.GetExportSha256(), "the export digest is checked and pinned by run.Run, not by the CLI")
}

func TestConformanceRun_AttendedWrapsEveryApprovalIngress(t *testing.T) {
	rig, _ := newConformanceStackRig(t)
	var captured run.Config
	rig.deps.conformance.run = capturingRun(&captured)

	out, err := rig.execute(t, rig.runArgs("--attended")...)
	require.NotErrorIs(t, err, constants.ErrEvaluationTargetAmbiguous)
	assert.Contains(t, out, "Suite: ", "the attended suite ran to a verdict")

	assert.True(t, captured.Attended)
	assert.IsType(t, &drive.Attended{}, captured.Drivers[cases.IngressMCP])
	assert.IsType(t, &drive.Attended{}, captured.Drivers[cases.IngressA2A])
	assert.IsType(t, &drive.Attended{}, captured.Drivers[cases.IngressDispatchEmbedded])
	assert.IsType(t, &drive.Attended{}, captured.Drivers[cases.IngressDispatchOutbound])
	assert.IsType(t, &drive.Attended{}, captured.Drivers[cases.IngressCLI])
	assert.IsType(t, &drive.Direct{}, captured.Drivers[cases.IngressDirect], "DIRECT carries no approval case and is never wrapped")
}

func TestConformanceRun_PrintsVerdictsFromStoredBytesNotFromMemory(t *testing.T) {
	rig, _ := newConformanceStackRig(t)
	rig = rig.wrapFiles(flipStoredRecord)

	out, err := rig.execute(t, rig.runArgs()...)
	require.ErrorIs(t, err, constants.ErrEvalRunVerificationFailed)
	assert.Contains(t, out, "Run: "+conformanceTestRunID)
	assert.Contains(t, out, "Suite: INVALID_EVIDENCE")
	assert.Contains(t, out, "RECORD_DIGEST_MISMATCH", "the verdict comes from the digest check on the stored bytes")
}

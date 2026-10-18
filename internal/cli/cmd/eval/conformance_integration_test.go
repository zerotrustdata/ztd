// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/drive"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/run"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	"github.com/zerotrustdata/ztd/internal/services/vault"
	"github.com/zerotrustdata/ztd/internal/testutil"
	harnessclient "github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// serveConformanceGateway serves the Gateway routes the conformance commands read:
// health for the posture, the Operator list, and the MCP tools/list the MCP driver checks.
// The server closes with the test; the returned value is its base URL.
func serveConformanceGateway(t *testing.T, posture constants.Posture, operators []*operatorv1.OperatorDocument) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc(constants.APIPaths.Health, func(w http.ResponseWriter, _ *http.Request) {
		writeConformanceTestJSON(t, w, models.HealthResponse{Posture: posture})
	})
	mux.HandleFunc(constants.APIPaths.Operators, func(w http.ResponseWriter, _ *http.Request) {
		writeConformanceTestJSON(t, w, models.OperatorSlotResponse{Success: true, Operators: operators})
	})
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, _ *http.Request) {
		result := struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}{Tools: []struct {
			Name string `json:"name"`
		}{{Name: (&mcp.RunShellCommandTool{}).Name()}, {Name: (&mcp.FileReadTool{}).Name()}}}
		raw, err := json.Marshal(result)
		if err != nil {
			t.Errorf("marshal tools/list: %v", err)
			return
		}
		writeConformanceTestJSON(t, w, harnessclient.JSONRPCResponse{JSONRPC: "2.0", ID: 1, Result: raw})
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server.URL
}

func writeConformanceTestJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("encode test response: %v", err)
	}
}

// newServedConformanceRig is newConformanceRig with a Gateway served over httptest.
func newServedConformanceRig(t *testing.T, opts rigOptions) conformanceRig {
	t.Helper()
	opts.serveGateway = serveConformanceGateway
	return newConformanceRig(t, opts)
}

func TestConformanceRun_UsesGatewayDataOperatorWithoutFlags(t *testing.T) {
	var captured run.Config
	embedded := &operatorv1.OperatorDocument{
		Id: string(constants.DocIDEmbeddedOperator), OperatorSessionId: conformanceTestEmbeddedSession,
		OperatorType: string(constants.OperatorTypeEmbedded), Status: string(constants.OperatorStatusActive),
		RuntimeConfig: &operatorv1.OperatorRuntimeConfig{},
	}
	rig := newServedConformanceRig(t, rigOptions{
		operators: []*operatorv1.OperatorDocument{embedded},
		run: func(_ context.Context, cfg run.Config) (*evalv1.ConformanceRunRecord, error) {
			captured = cfg
			return &evalv1.ConformanceRunRecord{RunId: cfg.RunID}, nil
		},
	})
	_, err := rig.execute(t, "conformance", "run")
	require.ErrorIs(t, err, constants.ErrEvalRunVerificationFailed, "the runner must be reached; this fake returns an unverifiable record")
	require.Len(t, captured.Roots, 1)
	assert.Equal(t, oracle.GatewayRuntimeID, captured.Roots[cases.ExecutorEmbedded].RuntimeID)
	require.Len(t, captured.Drivers, 3)
	require.Len(t, captured.Unavailable, 3)
	assert.Equal(t, cases.IngressCLI.UnavailableObservation(), captured.Unavailable[cases.IngressCLI])
}

func TestConformanceRun_PromptsAndDiscoversSelectedOperatorRoot(t *testing.T) {
	operators := conformanceSelectionOperators()
	var captured run.Config
	rig := newServedConformanceRig(t, rigOptions{operators: operators, run: func(_ context.Context, cfg run.Config) (*evalv1.ConformanceRunRecord, error) {
		captured = cfg
		return &evalv1.ConformanceRunRecord{RunId: cfg.RunID}, nil
	}})
	// The Gateway reads these documents when the command executes.
	for _, op := range operators {
		if op.GetId() == "beta" {
			op.LocalDir = rig.opRoot
		}
	}
	out, err := rig.executeInput(t, "2\n", "conformance", "run", "--attended")
	require.ErrorIs(t, err, constants.ErrEvalRunVerificationFailed)
	assert.Contains(t, out, "Choose a Data Operator")
	assert.Contains(t, out, "Data Operator: Beta")
	assert.Equal(t, "beta", captured.Roots[cases.ExecutorOutbound].RuntimeID)
	require.Len(t, captured.Drivers, 5)
	require.Len(t, captured.Unavailable, 1)
	assert.True(t, captured.Attended)
}

// flipRecordBytes serves the stored conformance record with one byte changed, so
// verification sees bytes that no longer match their sha256 sidecar.
type flipRecordBytes struct {
	fs.RuntimeFileService
}

func (f flipRecordBytes) ReadFile(ctx context.Context, relPath string) ([]byte, error) {
	raw, err := f.RuntimeFileService.ReadFile(ctx, relPath)
	if err != nil || len(raw) == 0 || !strings.HasSuffix(relPath, constants.ConformanceRecordFilename) {
		return raw, err
	}
	flipped := bytes.Clone(raw)
	flipped[len(flipped)/2] ^= 0x01
	return flipped, nil
}

func TestConformanceRun_TargetSelectionRequiresExactlyOneSessionOfEachTarget(t *testing.T) {
	embedded := &operatorv1.OperatorDocument{Id: string(constants.DocIDEmbeddedOperator), OperatorSessionId: conformanceTestEmbeddedSession}
	active := func(session string) *operatorv1.OperatorDocument {
		return &operatorv1.OperatorDocument{Id: conformanceTestOperatorID, OperatorSessionId: session, Status: string(constants.OperatorStatusActive), OperatorType: string(constants.OperatorTypeRemote)}
	}
	tests := []struct {
		name      string
		operators []*operatorv1.OperatorDocument
		wantErr   error
	}{
		{name: "no embedded operator", operators: []*operatorv1.OperatorDocument{active(conformanceTestOutboundSession)}, wantErr: constants.ErrEvaluationTargetUnavailable},
		{name: "no outbound operator", operators: []*operatorv1.OperatorDocument{embedded}, wantErr: constants.ErrEvaluationTargetUnavailable},
		{name: "outbound session not active", operators: []*operatorv1.OperatorDocument{embedded, {Id: conformanceTestOperatorID, OperatorSessionId: conformanceTestOutboundSession, Status: "stopped"}}, wantErr: constants.ErrEvaluationTargetUnavailable},
		{name: "two active outbound sessions", operators: []*operatorv1.OperatorDocument{embedded, active("session-a"), active("session-b")}, wantErr: constants.ErrEvaluationTargetAmbiguous},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rig := newServedConformanceRig(t, rigOptions{operators: tt.operators, run: func(context.Context, run.Config) (*evalv1.ConformanceRunRecord, error) {
				t.Fatal("run called with an unresolved target")
				return nil, nil
			}})
			_, err := rig.execute(t, rig.runArgs()...)
			assert.ErrorIs(t, err, tt.wantErr)
		})
	}
}

func TestConformanceRun_PassesConfiguredDriversAndRootsToRun(t *testing.T) {
	var captured run.Config
	rig := newServedConformanceRig(t, rigOptions{run: func(_ context.Context, cfg run.Config) (*evalv1.ConformanceRunRecord, error) {
		captured = cfg
		return &evalv1.ConformanceRunRecord{RunId: cfg.RunID, Posture: evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE}, nil
	}})

	_, err := rig.execute(t, rig.runArgs()...)
	require.ErrorIs(t, err, constants.ErrEvalRunVerificationFailed, "the empty record cannot verify, so the exit rule must fail it")

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
	assert.Equal(t, opRuntime.Resolve(""), captured.Roots[cases.ExecutorOutbound].Dir)
	assert.Equal(t, conformanceTestOperatorID, captured.Roots[cases.ExecutorOutbound].RuntimeID)
	assert.Nil(t, captured.Roots[cases.ExecutorEmbedded].Bound)
	require.NotNil(t, captured.Roots[cases.ExecutorOutbound].Bound, "the outbound anchor is the key bound at enrollment")
	assert.Equal(t, conformanceTestActuatorKey, captured.Roots[cases.ExecutorOutbound].Bound.GetPublicKeyHex())
}

func TestConformanceRun_OutboundWithoutEnrollmentBoundKeyFailsBeforeAnyCase(t *testing.T) {
	unbound := &operatorv1.OperatorDocument{Id: conformanceTestOperatorID, OperatorSessionId: conformanceTestOutboundSession, Status: string(constants.OperatorStatusActive), OperatorType: string(constants.OperatorTypeRemote)}
	operators := []*operatorv1.OperatorDocument{
		{Id: string(constants.DocIDEmbeddedOperator), OperatorSessionId: conformanceTestEmbeddedSession},
		unbound,
	}
	rig := newServedConformanceRig(t, rigOptions{operators: operators, run: func(context.Context, run.Config) (*evalv1.ConformanceRunRecord, error) {
		t.Fatal("run called for an outbound Operator with no enrollment-bound key")
		return nil, nil
	}})

	_, err := rig.execute(t, rig.runArgs()...)
	assert.ErrorIs(t, err, constants.ErrConformanceAnchorInvalid)
}

func TestConformanceRun_AttendedWrapsMCPAndA2AOnly(t *testing.T) {
	var captured run.Config
	rig := newServedConformanceRig(t, rigOptions{run: func(_ context.Context, cfg run.Config) (*evalv1.ConformanceRunRecord, error) {
		captured = cfg
		return &evalv1.ConformanceRunRecord{RunId: cfg.RunID, Attended: cfg.Attended}, nil
	}})

	_, err := rig.execute(t, rig.runArgs("--attended", "--approver-user-id", "approver-1")...)
	require.ErrorIs(t, err, constants.ErrEvalRunVerificationFailed)

	assert.True(t, captured.Attended)
	assert.IsType(t, &drive.Attended{}, captured.Drivers[cases.IngressMCP])
	assert.IsType(t, &drive.Attended{}, captured.Drivers[cases.IngressA2A])
	assert.IsType(t, &drive.Direct{}, captured.Drivers[cases.IngressDirect], "only MCP and A2A are wrapped")
	assert.IsType(t, &drive.CLI{}, captured.Drivers[cases.IngressCLI])
}

func TestConformanceRun_PrintsVerdictsFromStoredBytesNotFromMemory(t *testing.T) {
	rig := newServedConformanceRig(t, rigOptions{wrapGateway: func(files fs.RuntimeFileService) fs.RuntimeFileService {
		return flipRecordBytes{RuntimeFileService: files}
	}})

	out, err := rig.execute(t, rig.runArgs()...)
	require.ErrorIs(t, err, constants.ErrEvalRunVerificationFailed)
	assert.Contains(t, out, "Run: "+conformanceTestRunID)
	assert.Contains(t, out, "Suite: INVALID_EVIDENCE")
	assert.Contains(t, out, "RECORD_DIGEST_MISMATCH", "the verdict comes from the digest check on the stored bytes")
}

func TestConformanceVerify_FlippedByteIsInvalidEvidenceAndExitsNonZero(t *testing.T) {
	tests := []struct {
		name        string
		wrap        func(fs.RuntimeFileService) fs.RuntimeFileService
		wantInvalid bool
	}{
		{name: "stored bytes unchanged", wrap: nil, wantInvalid: false},
		{name: "one stored byte flipped", wrap: func(files fs.RuntimeFileService) fs.RuntimeFileService {
			return flipRecordBytes{RuntimeFileService: files}
		}, wantInvalid: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rig := newServedConformanceRig(t, rigOptions{wrapGateway: tt.wrap})
			saved, err := fs.NewRuntimeFileService(rig.gwRoot, testutil.NewTestLogger())
			require.NoError(t, err)
			record := &evalv1.ConformanceRunRecord{RunId: conformanceTestRunID, Posture: evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE}
			require.NoError(t, evaluation.NewStore(saved).SaveConformanceRecord(context.Background(), record))

			out, err := rig.execute(t, "conformance", "verify", "--run", conformanceTestRunID)
			if !tt.wantInvalid {
				assert.NotContains(t, out, "RECORD_DIGEST_MISMATCH", "an unchanged record must not fail the digest check")
				return
			}
			require.ErrorIs(t, err, constants.ErrEvalRunVerificationFailed)
			assert.Contains(t, out, "Suite: INVALID_EVIDENCE")
			assert.Contains(t, out, "RECORD_DIGEST_MISMATCH")
		})
	}
}

func TestConformanceVerify_FlippedByteInRealSealedRecordIsDigestMismatchVerdict(t *testing.T) {
	ctx := context.Background()
	sealed, _ := cmdtest.NewEvidenceCmdTestEnv(t)
	record := &evalv1.ConformanceRunRecord{RunId: conformanceTestRunID, Posture: evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE}
	require.NoError(t, evaluation.NewStore(sealed).SaveConformanceRecord(ctx, record))

	// One ciphertext byte changes on disk, under the sealing layer.
	recordPath := filepath.Join(constants.DataDirname, constants.EvaluationDirname, constants.EvaluationRunsDirname, conformanceTestRunID, constants.ConformanceRecordFilename)
	raw := evaluation.SealedFiles(sealed)
	ciphertext, err := raw.ReadFile(ctx, recordPath)
	require.NoError(t, err)
	flipped := bytes.Clone(ciphertext)
	flipped[len(flipped)/2] ^= 0x01
	require.NoError(t, raw.WriteFile(ctx, recordPath, flipped, constants.PermFilePrivate))

	rig := newConformanceRig(t, rigOptions{})
	rig.deps.fileSvcFactory = func(string, *slog.Logger) (fs.RuntimeFileService, error) { return sealed, nil }
	out, err := rig.execute(t, "conformance", "verify", "--run", conformanceTestRunID)
	require.ErrorIs(t, err, constants.ErrEvalRunVerificationFailed)
	assert.Contains(t, out, "Suite: INVALID_EVIDENCE")
	assert.Contains(t, out, "RECORD_DIGEST_MISMATCH", "the open vault refuses the changed ciphertext")

	// The same bytes under a locked vault are a locked-vault error, never a tamper verdict.
	locked, err := vault.NewVault(&vault.VaultConfig{FileSvc: raw, Logger: testutil.NewTestLogger()})
	require.NoError(t, err)
	lockedFiles, err := evaluation.NewEvidenceFileService(raw, func(fs.RuntimeFileService) (evaluation.EvidenceVault, error) { return locked, nil })
	require.NoError(t, err)
	rig.deps.fileSvcFactory = func(string, *slog.Logger) (fs.RuntimeFileService, error) { return lockedFiles, nil }
	out, err = rig.execute(t, "conformance", "verify", "--run", conformanceTestRunID)
	require.ErrorIs(t, err, constants.ErrVaultLocked)
	assert.NotContains(t, out, "Suite: ", "no verdicts are printed for a run the vault cannot open")
	assert.NotContains(t, out, "RECORD_DIGEST_MISMATCH", "a locked vault is not evidence that the bytes changed")
}

func TestConformanceVerify_JSONEmitsCanonicalVerdictSet(t *testing.T) {
	rig := newServedConformanceRig(t, rigOptions{wrapGateway: func(files fs.RuntimeFileService) fs.RuntimeFileService {
		return flipRecordBytes{RuntimeFileService: files}
	}})
	saved, err := fs.NewRuntimeFileService(rig.gwRoot, testutil.NewTestLogger())
	require.NoError(t, err)
	require.NoError(t, evaluation.NewStore(saved).SaveConformanceRecord(context.Background(), &evalv1.ConformanceRunRecord{RunId: conformanceTestRunID, Posture: evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE}))

	root := cmdtest.GlobalJSONRoot(t, evalCmdWithConfig(rig.deps))
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SilenceErrors = true
	root.SilenceUsage = true
	root.SetArgs([]string{"eval", "conformance", "verify", "--run", conformanceTestRunID})

	require.ErrorIs(t, root.Execute(), constants.ErrEvalRunVerificationFailed)
	var set evalv1.ConformanceVerdictSet
	require.NoError(t, evalv1.UnmarshalCanonical(bytes.TrimSpace(out.Bytes()), &set))
	assert.Equal(t, evalv1.ConformanceSuiteVerdict_CONFORMANCE_SUITE_VERDICT_INVALID_EVIDENCE, set.GetSuiteVerdict())
}

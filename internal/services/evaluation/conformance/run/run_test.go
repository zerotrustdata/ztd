// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/canary"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle/oracletest"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/testutil"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const (
	embeddedRuntimeID = "gateway"
	outboundRuntimeID = "op-outbound-1"
	testRunID         = "run-test"
)

// harness is the in-memory world a conformance run executes against. Every
// fake appends to one shared call log, so tests can assert the exact order of
// operations.
type harness struct {
	t *testing.T

	log  []string
	sink *[]client.Exchange // the recorder's current sink, nil outside a drive

	drivenIDs []string                // attempt IDs handed to a driver, in order
	attempts  []cases.Attempt         // attempts handed to a driver, in order
	driveErr  error                   // returned by every driver when set
	extraLink map[cases.Executor]bool // commit an unclaimed link after the first sentinel read
	forged    map[cases.Executor]bool // return a receipt whose signature does not verify

	signers map[cases.Executor]oracletest.Signer
	heads   map[cases.Executor]string
	reads   map[cases.Executor]int
	seq     int

	roots map[cases.Executor]Root
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		t:         t,
		extraLink: map[cases.Executor]bool{},
		forged:    map[cases.Executor]bool{},
		signers:   map[cases.Executor]oracletest.Signer{},
		heads:     map[cases.Executor]string{},
		reads:     map[cases.Executor]int{},
		roots:     map[cases.Executor]Root{},
	}
	for ex, runtimeID := range map[cases.Executor]string{
		cases.ExecutorEmbedded: embeddedRuntimeID,
		cases.ExecutorOutbound: outboundRuntimeID,
	} {
		signer := oracletest.NewSigner(t)
		h.signers[ex] = signer
		dir := testutil.TempDir(t)
		files, err := fs.NewRuntimeFileService(dir, nil)
		require.NoError(t, err)
		require.NoError(t, files.MkdirAll(context.Background(), constants.PkiDirname, constants.PermDirPrivate))
		export := fmt.Sprintf(`{"key_id":%q,"public_key":%q,"algorithm":"ed25519"}`,
			signer.KeyID, hex.EncodeToString(signer.Pub))
		require.NoError(t, files.WriteFile(context.Background(), actuatorExportPath(), []byte(export), constants.PermFilePrivate))
		h.heads[ex] = "genesis-" + string(ex)
		root := Root{
			Files:     loggingFiles{RuntimeFileService: files, h: h},
			Dir:       dir,
			RuntimeID: runtimeID,
		}
		if ex == cases.ExecutorOutbound {
			// The outbound key is bound at enrollment, and its export must match it.
			root.Bound = signer.Anchor(runtimeID)
		}
		h.roots[ex] = root
	}
	return h
}

// canaryRoot is the runtime-relative canary root the runner seeds for testRunID.
func canaryRoot() string {
	return path.Join(constants.EvaluationControlledTargetDirname, constants.EvaluationConformanceCanaryPrefix+testRunID)
}

// loggingFiles records a "snapshot" for every Lstat of the canary root, which
// is how canary.Snapshot and canary.Seed reach the file service.
type loggingFiles struct {
	fs.RuntimeFileService
	h *harness
}

func (f loggingFiles) Lstat(ctx context.Context, relPath string) (os.FileInfo, error) {
	if relPath == canaryRoot() {
		f.h.log = append(f.h.log, "snapshot")
	}
	return f.RuntimeFileService.Lstat(ctx, relPath)
}

// commit appends one executed commitment link to ex's ledger and returns its
// signed receipt.
func (h *harness) commit(ex cases.Executor, actionType string) *operatorv1.ActionReceipt {
	h.seq++
	prior := h.heads[ex]
	hash := fmt.Sprintf("hash-%s-%d", ex, h.seq)
	runtimeID := h.roots[ex].RuntimeID
	receipt := h.signers[ex].Executed(h.t, oracletest.ReceiptOpts{
		TransactionID: fmt.Sprintf("tx-%s-%d", ex, h.seq),
		OperatorID:    runtimeID,
		SessionID:     "sess-1",
		ActionType:    actionType,
		L2:            operatorv1.L2Status_L2_STATUS_REQUIRED_VALID,
		L3:            operatorv1.L3Status_L3_STATUS_NOT_REQUIRED,
	}, prior, hash)
	h.heads[ex] = hash
	return receipt
}

// Read is the cases.Sentinel fake: every read commits one link, and an extra
// unclaimed link follows the first read when extraLink is set for ex.
func (h *harness) Read(_ context.Context, ex cases.Executor, _ string) (*operatorv1.ActionReceipt, error) {
	h.log = append(h.log, "sentinel:"+string(ex))
	h.reads[ex]++
	receipt := h.commit(ex, "FS_READ")
	if h.extraLink[ex] && h.reads[ex] == 1 {
		h.commit(ex, "FILE_EDIT")
	}
	if h.forged[ex] {
		receipt.Signature = "00" + receipt.Signature[2:]
	}
	return receipt, nil
}

// driver is the cases.Driver fake for one ingress. It records the attempt and
// stands in for the recorder by appending exchanges while a sink is set.
type driver struct {
	h *harness
}

func (d driver) Drive(_ context.Context, a cases.Attempt) (cases.Driven, error) {
	d.h.log = append(d.h.log, "drive:"+a.AttemptID)
	d.h.drivenIDs = append(d.h.drivenIDs, a.AttemptID)
	d.h.attempts = append(d.h.attempts, a)
	if d.h.driveErr != nil {
		return cases.Driven{}, d.h.driveErr
	}
	if d.h.sink != nil {
		*d.h.sink = append(*d.h.sink, client.Exchange{
			Method:    http.MethodPost,
			URL:       "/api/" + a.AttemptID,
			Status:    http.StatusAccepted,
			ReqBody:   []byte(`{"attempt":"` + a.AttemptID + `"}`),
			RespRaw:   "plain " + a.AttemptID,
			Persona:   "unused",
			At:        time.Time{},
			Err:       "",
			LatencyMS: 0,
		})
	}
	return cases.Driven{}, nil
}

// config builds a run config over the harness for posture, with a drivers map
// covering every ingress.
func (h *harness) config(posture evalv1.EvaluationGovernancePosture, attended bool) Config {
	drivers := map[cases.Ingress]cases.Driver{}
	for _, ingress := range []cases.Ingress{
		cases.IngressDirect, cases.IngressDispatchEmbedded, cases.IngressDispatchOutbound,
		cases.IngressMCP, cases.IngressA2A, cases.IngressCLI,
	} {
		drivers[ingress] = driver{h: h}
	}
	return Config{
		RunID:    testRunID,
		Drivers:  drivers,
		Sentinel: h,
		Roots:    h.roots,
		Posture: func(context.Context) (evalv1.EvaluationGovernancePosture, error) {
			return posture, nil
		},
		A2AIngress: true,
		Recorder:   func(sink *[]client.Exchange) { h.sink = sink },
		Attended:   attended,
		Now:        fixedClock(),
	}
}

func fixedClock() func() time.Time {
	base := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return base }
}

// expectedLog is the call log a correct run produces: one canary seed per
// executor, two quiescence sentinels per executor, then per driven case the
// bracket snapshot, sentinel, drive, sentinel, snapshot. Attended cases in an
// unattended run produce no calls.
func expectedLog(record *evalv1.ConformanceRunRecord) []string {
	var executors []string
	seen := map[string]bool{}
	for _, c := range record.GetCases() {
		if !seen[c.GetExecutor()] {
			seen[c.GetExecutor()] = true
			executors = append(executors, c.GetExecutor())
		}
	}
	var want []string
	for range executors {
		want = append(want, "snapshot")
	}
	for _, ex := range executors {
		want = append(want, "sentinel:"+ex, "sentinel:"+ex)
	}
	for _, c := range record.GetCases() {
		if isNotRun(c) {
			continue
		}
		ex := "sentinel:" + c.GetExecutor()
		want = append(want, "snapshot", ex, "drive:"+c.GetAttemptId(), ex, "snapshot")
	}
	return want
}

func isNotRun(c *evalv1.ConformanceCaseEvidence) bool {
	return len(c.GetObservations()) == 1 &&
		c.GetObservations()[0].GetKind() == evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_NOT_RUN_ATTENDED
}

func TestRunOrdersEachCaseBetweenCanarySnapshotsAndSentinels(t *testing.T) {
	h := newHarness(t)

	record, err := Run(context.Background(), h.config(evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE, true))
	require.NoError(t, err)

	require.NotEmpty(t, record.GetCases())
	assert.Equal(t, expectedLog(record), h.log)
	assert.Len(t, record.GetQuiescence(), 2)
}

func TestRunAttemptTargetRelIsCanaryRelativeCaseFile(t *testing.T) {
	h := newHarness(t)

	record, err := Run(context.Background(), h.config(evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE, true))
	require.NoError(t, err)

	require.NotEmpty(t, h.attempts)
	for _, a := range h.attempts {
		assert.Equal(t, canary.TargetRel(a.AttemptID), a.TargetRel, "attempt %s", a.AttemptID)
		assert.False(t, strings.HasPrefix(a.TargetRel, canaryRoot()), "attempt %s target is root-prefixed: %s", a.AttemptID, a.TargetRel)
	}
	for _, c := range record.GetCases() {
		if isNotRun(c) {
			continue
		}
		assert.Equal(t, canary.TargetRel(c.GetAttemptId()), c.GetTargetPath(), "case %s", c.GetCaseId())
	}
}

func TestRunRecordsFilledHeaderAndAnchors(t *testing.T) {
	h := newHarness(t)

	record, err := Run(context.Background(), h.config(evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE, true))
	require.NoError(t, err)

	assert.Equal(t, testRunID, record.GetRunId())
	assert.Equal(t, cases.SuiteID, record.GetSuiteId())
	assert.Equal(t, cases.SuiteVersion, record.GetSuiteVersion())
	assert.Equal(t, evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE, record.GetPosture())
	assert.Equal(t, canaryRoot(), record.GetCanaryRoot())
	assert.True(t, record.GetAttended())
	assert.Equal(t, fixedClock()(), record.GetStartedAt().AsTime())
	assert.Equal(t, fixedClock()(), record.GetFinishedAt().AsTime())
	require.Len(t, record.GetAnchors(), 2)
	assert.Equal(t, embeddedRuntimeID, record.GetAnchors()[0].GetRuntimeId())
	assert.Equal(t, outboundRuntimeID, record.GetAnchors()[1].GetRuntimeId())
	assert.Len(t, record.GetDoctrineCorpusSha256(), 64)
}

// actuatorExportPath is the actuator export under a runtime root.
func actuatorExportPath() string {
	return filepath.Join(constants.PkiDirname, constants.ActuatorPubJSONFilename)
}

// writeActuatorExport replaces the actuator export of ex's runtime with one that names signer's key.
func writeActuatorExport(t *testing.T, h *harness, ex cases.Executor, signer oracletest.Signer) {
	t.Helper()
	export := fmt.Sprintf(`{"key_id":%q,"public_key":%q,"algorithm":"ed25519"}`, signer.KeyID, hex.EncodeToString(signer.Pub))
	require.NoError(t, h.roots[ex].Files.WriteFile(context.Background(), actuatorExportPath(), []byte(export), constants.PermFilePrivate))
}

func TestRunPinsOutboundAnchorToEnrollmentBindingAndExportDigest(t *testing.T) {
	h := newHarness(t)

	record, err := Run(context.Background(), h.config(evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE, true))
	require.NoError(t, err)

	root := h.roots[cases.ExecutorOutbound]
	export, err := root.Files.ReadFile(context.Background(), actuatorExportPath())
	require.NoError(t, err)
	digest := sha256.Sum256(export)
	outbound := record.GetAnchors()[1]
	assert.Equal(t, outboundRuntimeID, outbound.GetRuntimeId())
	assert.Equal(t, root.Bound.GetKeyId(), outbound.GetKeyId())
	assert.Equal(t, root.Bound.GetPublicKeyHex(), outbound.GetPublicKeyHex())
	assert.Equal(t, hex.EncodeToString(digest[:]), outbound.GetExportSha256())
}

func TestRunRefusesOutboundAnchorThatDoesNotCheckAgainstItsExport(t *testing.T) {
	other := oracletest.NewSigner(t)
	tests := []struct {
		name   string
		mutate func(t *testing.T, h *harness)
		want   error
	}{
		{name: "export names a key other than the enrollment-bound key", want: constants.ErrConformanceAnchorInvalid, mutate: func(t *testing.T, h *harness) {
			writeActuatorExport(t, h, cases.ExecutorOutbound, other)
		}},
		{name: "export missing", want: constants.ErrConformanceAnchorMissing, mutate: func(t *testing.T, h *harness) {
			require.NoError(t, h.roots[cases.ExecutorOutbound].Files.Remove(context.Background(), actuatorExportPath()))
		}},
		{name: "bound anchor names another runtime", want: constants.ErrConformanceAnchorInvalid, mutate: func(t *testing.T, h *harness) {
			root := h.roots[cases.ExecutorOutbound]
			root.Bound = h.signers[cases.ExecutorOutbound].Anchor("op-other")
			h.roots[cases.ExecutorOutbound] = root
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			tt.mutate(t, h)

			record, err := Run(context.Background(), h.config(evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE, true))

			require.Error(t, err)
			assert.ErrorIs(t, err, tt.want)
			assert.Nil(t, record)
			assert.Empty(t, h.log, "the anchor is checked before any canary seed, sentinel read or drive")
		})
	}
}

func TestRunRequiresEnrollmentBoundAnchorOnlyForOutbound(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(h *harness)
		want   error
	}{
		{name: "outbound root without a bound anchor is refused", want: constants.ErrMissingRequiredField, mutate: func(h *harness) {
			root := h.roots[cases.ExecutorOutbound]
			root.Bound = nil
			h.roots[cases.ExecutorOutbound] = root
		}},
		{name: "embedded root with a bound anchor is refused", want: constants.ErrConformanceAnchorInvalid, mutate: func(h *harness) {
			root := h.roots[cases.ExecutorEmbedded]
			root.Bound = h.signers[cases.ExecutorEmbedded].Anchor(embeddedRuntimeID)
			h.roots[cases.ExecutorEmbedded] = root
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			tt.mutate(h)

			record, err := Run(context.Background(), h.config(evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE, true))

			require.Error(t, err)
			assert.ErrorIs(t, err, tt.want)
			assert.Nil(t, record)
			assert.Empty(t, h.log, "the anchor is checked before any canary seed, sentinel read or drive")
		})
	}
}

func TestRunAbortsWhenStackIsNotQuiescent(t *testing.T) {
	h := newHarness(t)
	h.extraLink[cases.ExecutorEmbedded] = true

	record, err := Run(context.Background(), h.config(evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE, true))

	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrConformanceStackNotQuiescent)
	assert.ErrorIs(t, err, constants.ErrConformanceUnclaimedExecution)
	assert.Nil(t, record)
}

func TestRunAbortsWhenSentinelReceiptIsForged(t *testing.T) {
	h := newHarness(t)
	h.forged[cases.ExecutorOutbound] = true

	record, err := Run(context.Background(), h.config(evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE, true))

	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrConformanceStackNotQuiescent)
	assert.Nil(t, record)
	assert.Equal(t, []string{"snapshot", "snapshot", "sentinel:embedded", "sentinel:embedded", "sentinel:outbound", "sentinel:outbound"}, h.log)
}

func TestRunRecordsAttendedCaseAsNotRunInUnattendedRun(t *testing.T) {
	h := newHarness(t)
	applicable, err := cases.Applicable(evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_RATIFY, cases.Stack{A2AIngress: true, Outbound: true})
	require.NoError(t, err)
	var attended int
	for _, c := range applicable {
		if c.Attended {
			attended++
		}
	}
	require.Positive(t, attended, "ratify must carry attended cases for this test to mean anything")

	record, err := Run(context.Background(), h.config(evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_RATIFY, false))
	require.NoError(t, err)

	var notRun int
	for _, c := range record.GetCases() {
		if !isNotRun(c) {
			continue
		}
		notRun++
		assert.Len(t, c.GetObservations(), 1)
		assert.Nil(t, c.GetSentinelBefore())
		assert.Nil(t, c.GetSentinelAfter())
		assert.Nil(t, c.GetCanaryBefore())
		assert.Nil(t, c.GetCanaryAfter())
		assert.Empty(t, c.GetExchanges())
		assert.NotContains(t, h.drivenIDs, c.GetAttemptId())
	}
	assert.Equal(t, attended, notRun)
	assert.Equal(t, expectedLog(record), h.log)
}

func TestRunWithEmbeddedOperatorOnlyOmitsOutboundCases(t *testing.T) {
	h := newHarness(t)
	cfg := h.config(evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE, false)
	delete(cfg.Roots, cases.ExecutorOutbound)
	delete(cfg.Drivers, cases.IngressDispatchOutbound)
	delete(cfg.Drivers, cases.IngressCLI)
	delete(cfg.Drivers, cases.IngressA2A)
	cfg.Unavailable = map[cases.Ingress]evalv1.ConformanceObservationKind{cases.IngressA2A: cases.IngressA2A.UnavailableObservation()}
	record, err := Run(context.Background(), cfg)
	require.NoError(t, err)
	require.Len(t, record.GetAnchors(), 1)
	require.Len(t, record.GetQuiescence(), 1)
	assert.Zero(t, h.reads[cases.ExecutorOutbound])
	assert.True(t, record.GetOutboundAbsent())
	assert.False(t, record.GetA2AIngressAbsent())
	applicable, err := cases.Applicable(record.GetPosture(), cases.RecordStack(record))
	require.NoError(t, err)
	require.Len(t, record.GetCases(), len(applicable))
	for _, c := range applicable {
		assert.NotEqual(t, cases.ExecutorOutbound, c.Ingress.Executor(), c.ID)
	}
	for i, c := range applicable {
		if kind, skipped := cfg.Unavailable[c.Ingress]; skipped {
			ev := record.GetCases()[i]
			require.Len(t, ev.GetObservations(), 1)
			assert.Equal(t, kind, ev.GetObservations()[0].GetKind())
			assert.Nil(t, ev.GetSentinelBefore())
			assert.Nil(t, ev.GetCanaryBefore())
		}
	}
	for _, a := range h.attempts {
		assert.NotContains(t, cfg.Unavailable, a.Case.Ingress)
	}
}

func TestRunFailsBeforeSeedingWhenIngressCannotBeDriven(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(cfg *Config)
	}{
		{name: "missing driver", mutate: func(cfg *Config) { delete(cfg.Drivers, cases.IngressMCP) }},
		{name: "missing outbound driver", mutate: func(cfg *Config) { delete(cfg.Drivers, cases.IngressCLI) }},
		{name: "missing embedded root", mutate: func(cfg *Config) { delete(cfg.Roots, cases.ExecutorEmbedded) }},
		{name: "required ingress cannot be skipped", mutate: func(cfg *Config) {
			delete(cfg.Drivers, cases.IngressMCP)
			cfg.Unavailable = map[cases.Ingress]evalv1.ConformanceObservationKind{cases.IngressMCP: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_NOT_RUN_A2A}
		}},
		{name: "unavailable driver also configured", mutate: func(cfg *Config) {
			cfg.Unavailable = map[cases.Ingress]evalv1.ConformanceObservationKind{cases.IngressA2A: cases.IngressA2A.UnavailableObservation()}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			cfg := h.config(evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE, true)
			embedded := h.roots[cases.ExecutorEmbedded]
			tt.mutate(&cfg)

			record, err := Run(context.Background(), cfg)

			require.Error(t, err)
			assert.ErrorIs(t, err, constants.ErrConformanceIngressUnsupported)
			assert.Nil(t, record)
			assert.Empty(t, h.log)
			_, err = embedded.Files.Lstat(context.Background(), canaryRoot())
			assert.ErrorIs(t, err, constants.ErrNotFound)
		})
	}
}

func TestRunAbortsWithNoRecordWhenDriverFails(t *testing.T) {
	h := newHarness(t)
	driveErr := errors.New("transport down")
	h.driveErr = driveErr

	record, err := Run(context.Background(), h.config(evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE, true))

	require.Error(t, err)
	assert.ErrorIs(t, err, driveErr)
	assert.Nil(t, record)
}

func TestRunAttachesExchangesOnlyToTheirOwnCase(t *testing.T) {
	h := newHarness(t)

	record, err := Run(context.Background(), h.config(evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE, true))
	require.NoError(t, err)

	require.NotEmpty(t, record.GetCases())
	for _, c := range record.GetCases() {
		require.Len(t, c.GetExchanges(), 1, "case %s", c.GetCaseId())
		exchange := c.GetExchanges()[0]
		assert.Equal(t, "/api/"+c.GetAttemptId(), exchange.GetUrl())
		assert.Equal(t, http.MethodPost, exchange.GetMethod())
		assert.Equal(t, int32(http.StatusAccepted), exchange.GetStatus())
		assert.Equal(t, `{"attempt":"`+c.GetAttemptId()+`"}`, string(exchange.GetRequestBody()))
		assert.Equal(t, "plain "+c.GetAttemptId(), string(exchange.GetResponseBody()))
	}
	assert.Nil(t, h.sink, "recorder must be detached after the run")
}

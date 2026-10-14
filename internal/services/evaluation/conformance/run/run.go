// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package run

import (
	"context"
	"fmt"
	"path/filepath"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/canary"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle"
	"github.com/zerotrustdata/ztd/internal/services/governance/doctrinecorpus"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// Run executes the conformance suite for the posture reported by cfg.Posture
// and returns its raw evidence. Any error aborts the run and no partial record
// is returned.
func Run(ctx context.Context, cfg Config) (*evalv1.ConformanceRunRecord, error) {
	if cfg.Sentinel == nil || cfg.Posture == nil || cfg.Recorder == nil || cfg.Now == nil {
		return nil, fmt.Errorf("conformance run: config incomplete: %w", constants.ErrMissingRequiredField)
	}
	started := cfg.Now()

	cs, gaps, err := cases.Catalogue()
	if err != nil {
		return nil, fmt.Errorf("conformance run: catalogue: %w", err)
	}
	corpus, err := doctrinecorpus.Load()
	if err != nil {
		return nil, fmt.Errorf("conformance run: doctrine corpus: %w", err)
	}
	if err := cases.Validate(cs, gaps, corpus); err != nil {
		return nil, fmt.Errorf("conformance run: validate catalogue: %w", err)
	}

	posture, err := cfg.Posture(ctx)
	if err != nil {
		return nil, fmt.Errorf("conformance run: read posture: %w", err)
	}
	if posture == evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_UNSPECIFIED {
		return nil, fmt.Errorf("conformance run: read posture: %w", constants.ErrConformancePostureUnknown)
	}
	applicable, err := cases.Applicable(posture)
	if err != nil {
		return nil, fmt.Errorf("conformance run: applicable cases: %w", err)
	}

	executors, err := usedExecutors(cfg, applicable)
	if err != nil {
		return nil, err
	}

	anchors := make([]*evalv1.ConformanceAnchor, 0, len(executors))
	for _, ex := range executors {
		root := cfg.Roots[ex]
		anchor, err := oracle.LoadAnchor(ctx, root.Files, root.RuntimeID)
		if err != nil {
			return nil, fmt.Errorf("conformance run: load anchor for %s: %w", ex, err)
		}
		anchors = append(anchors, anchor)
	}
	anchorSet, err := oracle.NewAnchorSet(anchors)
	if err != nil {
		return nil, fmt.Errorf("conformance run: anchor set: %w", err)
	}

	sets := make(map[cases.Executor]*canary.Set, len(executors))
	for _, ex := range executors {
		set, err := canary.Seed(ctx, cfg.Roots[ex].Files, cfg.RunID)
		if err != nil {
			return nil, fmt.Errorf("conformance run: seed canary for %s: %w", ex, err)
		}
		sets[ex] = set
	}
	canaryRoot := sets[executors[0]].Root

	quiescence := make([]*evalv1.ConformanceQuiescence, 0, len(executors))
	for _, ex := range executors {
		proof, err := quiesce(ctx, cfg, anchorSet, ex, sets[ex])
		if err != nil {
			return nil, err
		}
		quiescence = append(quiescence, proof)
	}

	evidence := make([]*evalv1.ConformanceCaseEvidence, 0, len(applicable))
	for index, c := range applicable {
		attemptID := fmt.Sprintf("%s-%04d", cfg.RunID, index+1)
		ex := c.Ingress.Executor()
		root := cfg.Roots[ex]
		set := sets[ex]
		attempt := cases.Attempt{
			RunID:     cfg.RunID,
			AttemptID: attemptID,
			Case:      c,
			TargetAbs: filepath.Join(root.Dir, set.TargetPath(attemptID)),
			TargetRel: set.TargetPath(attemptID),
			ReadAbs:   filepath.Join(root.Dir, set.ReadPath()),
			Marker:    cases.Marker(cfg.RunID, attemptID),
		}
		if c.Attended && !cfg.Attended {
			evidence = append(evidence, notRunEvidence(attempt))
			continue
		}
		item, err := driveCase(ctx, cfg, root, set, attempt)
		if err != nil {
			return nil, err
		}
		evidence = append(evidence, item)
	}

	return &evalv1.ConformanceRunRecord{
		RunId:                cfg.RunID,
		SuiteId:              cases.SuiteID,
		SuiteVersion:         cases.SuiteVersion,
		Posture:              posture,
		Anchors:              anchors,
		CanaryRoot:           canaryRoot,
		Quiescence:           quiescence,
		Cases:                evidence,
		DoctrineCorpusSha256: doctrinecorpus.SHA256(),
		Attended:             cfg.Attended,
		StartedAt:            timestamppb.New(started),
		FinishedAt:           timestamppb.New(cfg.Now()),
	}, nil
}

// usedExecutors lists the executors the applicable cases run on, in catalogue
// order, and fails before any side effect if a case has no driver or no root.
func usedExecutors(cfg Config, applicable []cases.Case) ([]cases.Executor, error) {
	var executors []cases.Executor
	seen := make(map[cases.Executor]struct{})
	for _, c := range applicable {
		if _, ok := cfg.Drivers[c.Ingress]; !ok {
			return nil, fmt.Errorf("conformance run: case %s: no driver for ingress %s: %w", c.ID, c.Ingress, constants.ErrConformanceIngressUnsupported)
		}
		ex := c.Ingress.Executor()
		if _, ok := cfg.Roots[ex]; !ok {
			return nil, fmt.Errorf("conformance run: case %s: no root for executor %s: %w", c.ID, ex, constants.ErrConformanceIngressUnsupported)
		}
		if _, ok := seen[ex]; !ok {
			seen[ex] = struct{}{}
			executors = append(executors, ex)
		}
	}
	return executors, nil
}

// quiesce reads two sentinels in a row on one executor and records both. The
// stack is quiet only when both receipts are executed and nothing else was
// committed between them.
func quiesce(ctx context.Context, cfg Config, anchors *oracle.AnchorSet, ex cases.Executor, set *canary.Set) (*evalv1.ConformanceQuiescence, error) {
	root := cfg.Roots[ex]
	readAbs := filepath.Join(root.Dir, set.ReadPath())
	first, err := cfg.Sentinel.Read(ctx, ex, readAbs)
	if err != nil {
		return nil, fmt.Errorf("conformance run: quiescence read on %s: %w", ex, err)
	}
	second, err := cfg.Sentinel.Read(ctx, ex, readAbs)
	if err != nil {
		return nil, fmt.Errorf("conformance run: quiescence read on %s: %w", ex, err)
	}
	firstOutcome, err := executedOutcome(anchors, root.RuntimeID, first)
	if err != nil {
		return nil, fmt.Errorf("conformance run: quiescence on %s: %w: %w", ex, constants.ErrConformanceStackNotQuiescent, err)
	}
	secondOutcome, err := executedOutcome(anchors, root.RuntimeID, second)
	if err != nil {
		return nil, fmt.Errorf("conformance run: quiescence on %s: %w: %w", ex, constants.ErrConformanceStackNotQuiescent, err)
	}
	if err := oracle.VerifyBracket(firstOutcome, secondOutcome, nil); err != nil {
		return nil, fmt.Errorf("conformance run: quiescence on %s: %w: %w", ex, constants.ErrConformanceStackNotQuiescent, err)
	}
	return &evalv1.ConformanceQuiescence{Executor: string(ex), First: first, Second: second}, nil
}

// executedOutcome verifies a sentinel receipt and requires it to be an executed
// outcome, which is the only shape a sentinel read may take.
func executedOutcome(anchors *oracle.AnchorSet, runtimeID string, receipt *operatorv1.ActionReceipt) (oracle.Outcome, error) {
	if _, err := oracle.VerifyReceipt(anchors, runtimeID, receipt); err != nil {
		return oracle.Outcome{}, err
	}
	outcome, err := oracle.Classify(receipt)
	if err != nil {
		return oracle.Outcome{}, err
	}
	if outcome.Kind != oracle.OutcomeExecuted {
		return oracle.Outcome{}, fmt.Errorf("sentinel receipt outcome %d is not executed", outcome.Kind)
	}
	return outcome, nil
}

// notRunEvidence records an attended case skipped by an unattended run. It
// takes no sentinels, snapshots, or drive.
func notRunEvidence(attempt cases.Attempt) *evalv1.ConformanceCaseEvidence {
	return &evalv1.ConformanceCaseEvidence{
		CaseId:       attempt.Case.ID,
		AttemptId:    attempt.AttemptID,
		Executor:     string(attempt.Case.Ingress.Executor()),
		TargetPath:   attempt.TargetRel,
		Marker:       attempt.Marker,
		Observations: []*evalv1.ConformanceObservation{{Kind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_NOT_RUN_ATTENDED}},
	}
}

// driveCase brackets one drive with canary snapshots and sentinel reads. Only
// the drive is exchange-captured, so exchanges hold this case's own traffic.
func driveCase(ctx context.Context, cfg Config, root Root, set *canary.Set, attempt cases.Attempt) (*evalv1.ConformanceCaseEvidence, error) {
	ex := attempt.Case.Ingress.Executor()
	canaryBefore, err := canary.Snapshot(ctx, root.Files, set.Root)
	if err != nil {
		return nil, fmt.Errorf("conformance run: case %s canary before: %w", attempt.Case.ID, err)
	}
	sentinelBefore, err := cfg.Sentinel.Read(ctx, ex, attempt.ReadAbs)
	if err != nil {
		return nil, fmt.Errorf("conformance run: case %s sentinel before: %w", attempt.Case.ID, err)
	}

	var exchanges []client.Exchange
	cfg.Recorder(&exchanges)
	driven, err := cfg.Drivers[attempt.Case.Ingress].Drive(ctx, attempt)
	cfg.Recorder(nil)
	if err != nil {
		return nil, fmt.Errorf("conformance run: case %s drive: %w", attempt.Case.ID, err)
	}

	sentinelAfter, err := cfg.Sentinel.Read(ctx, ex, attempt.ReadAbs)
	if err != nil {
		return nil, fmt.Errorf("conformance run: case %s sentinel after: %w", attempt.Case.ID, err)
	}
	canaryAfter, err := canary.Snapshot(ctx, root.Files, set.Root)
	if err != nil {
		return nil, fmt.Errorf("conformance run: case %s canary after: %w", attempt.Case.ID, err)
	}

	return &evalv1.ConformanceCaseEvidence{
		CaseId:         attempt.Case.ID,
		AttemptId:      attempt.AttemptID,
		Executor:       string(ex),
		TargetPath:     attempt.TargetRel,
		Marker:         attempt.Marker,
		Observations:   driven.Observations,
		SentinelBefore: sentinelBefore,
		SentinelAfter:  sentinelAfter,
		CanaryBefore:   canaryBefore,
		CanaryAfter:    canaryAfter,
		A2ACalls:       driven.A2ACalls,
		Exchanges:      convertExchanges(exchanges),
	}, nil
}

// convertExchanges maps captured HTTP exchanges onto the record shape. A body
// is either valid JSON (ReqBody/RespBody) or raw text (ReqRaw/RespRaw), never
// both, so the first non-empty representation is the whole body.
func convertExchanges(in []client.Exchange) []*evalv1.ConformanceExchange {
	out := make([]*evalv1.ConformanceExchange, 0, len(in))
	for _, ex := range in {
		out = append(out, &evalv1.ConformanceExchange{
			Method:       ex.Method,
			Url:          ex.URL,
			Status:       int32(ex.Status),
			RequestBody:  exchangeBody(ex.ReqBody, ex.ReqRaw),
			ResponseBody: exchangeBody(ex.RespBody, ex.RespRaw),
		})
	}
	return out
}

func exchangeBody(encoded []byte, raw string) []byte {
	if len(encoded) > 0 {
		return encoded
	}
	return []byte(raw)
}

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package pubsub

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/scrubbing"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// egressClass is the egress classification of one result payload type. Every payload type that
// can leave the runtime has exactly one class; an unlisted type is an error, never a pass-through.
type egressClass uint8

const (
	// egressNoContent: the type carries only identifiers, hashes, signatures, counts, enums,
	// timestamps or host telemetry. Nothing is scrubbed.
	egressNoContent egressClass = iota + 1
	// egressContent: the type carries command lines, output, file contents or paths, commit
	// messages, model names, error text or other content. Its content fields are scrubbed.
	egressContent
	// egressDigestBound: the type is verified by the Gateway against a digest or signature that
	// covers its fields (InferenceCompletion result_digest bound by the L5 event receipt, observation
	// and attestation digests, InferenceProgressEvent parts bound to the terminal output hash).
	// Scrubbing at egress would break verification. Inference output must be scrubbed at its
	// source before its digest is computed and the event receipt signed, not at egress.
	egressDigestBound
)

// egressScrubbed returns a scrubbed copy of a payload that is about to leave the runtime.
//
// This is the single egress choke point: BuildUniversalResultEnvelope calls it before any result
// payload is marshalled into a GovernanceEnvelope. Every content-bearing string is passed through
// ScrubText exactly once. The caller's payload is never mutated.
//
// Failures (all fail closed, no raw fallback):
//   - an unclassified payload type yields an error wrapping constants.ErrEgressPayloadUnclassified;
//   - a payload with non-empty content and a nil or disabled scrubber yields an error wrapping
//     constants.ErrEgressScrubberUnavailable.
func egressScrubbed(ctx context.Context, payload proto.Message, s *scrubbing.ScrubbingService) (proto.Message, error) {
	clone := proto.Clone(payload)
	fields, class, err := egressContentFields(clone)
	if err != nil {
		return nil, fmt.Errorf("pubsub: egress scrub: %w", err)
	}
	if class != egressContent {
		return clone, nil
	}

	var hasContent bool
	for _, f := range fields {
		if *f != "" {
			hasContent = true
			break
		}
	}
	if !hasContent {
		return clone, nil
	}
	if s == nil || !s.IsEnabled() {
		return nil, fmt.Errorf("pubsub: egress scrub: %w", constants.ErrEgressScrubberUnavailable)
	}

	for _, f := range fields {
		if *f != "" {
			*f = s.ScrubText(ctx, *f)
		}
	}
	return clone, nil
}

// egressContentFields classifies payload and returns pointers to every content-bearing string in it,
// so that scrubbing and the content check share one list. The class is returned for every
// classified type. An unclassified type returns constants.ErrEgressPayloadUnclassified.
//
// Adding a result payload type requires adding a case here; the table test in egress_test.go
// fails for any result type that is not listed.
func egressContentFields(payload proto.Message) (fields []*string, class egressClass, err error) {
	switch p := payload.(type) {
	case *operatorv1.CommandResult:
		return []*string{&p.Stdout, &p.Stderr, &p.Error}, egressContent, nil
	case *operatorv1.ExecutionStatusUpdate:
		return []*string{&p.Command, &p.NewOutput, &p.NewStderr, &p.Message}, egressContent, nil
	case *operatorv1.FileEditResult:
		return []*string{&p.FilePath, &p.BackupPath, &p.Content, &p.ErrorMessage, &p.ErrorType}, egressContent, nil
	case *operatorv1.FsListResult:
		fields = []*string{&p.Path}
		for _, e := range p.Entries {
			fields = append(fields, &e.Name, &e.Path)
		}
		return append(fields, &p.ErrorMessage, &p.ErrorType), egressContent, nil
	case *operatorv1.FsGrepResult:
		fields = []*string{&p.Path}
		for _, m := range p.Matches {
			fields = append(fields, &m.Path, &m.Content)
			for i := range m.Before {
				fields = append(fields, &m.Before[i])
			}
			for i := range m.After {
				fields = append(fields, &m.After[i])
			}
		}
		return append(fields, &p.ErrorMessage, &p.ErrorType), egressContent, nil
	case *operatorv1.FsReadResult:
		return []*string{&p.Path, &p.Content, &p.ErrorMessage, &p.ErrorType}, egressContent, nil
	case *operatorv1.PortCheckResult:
		fields = nil
		for _, r := range p.Results {
			fields = append(fields, &r.Host, &r.Error)
		}
		return append(fields, &p.ErrorMessage, &p.ErrorType), egressContent, nil
	case *operatorv1.OllamaModelInventoryResult:
		fields = nil
		for _, e := range p.Entries {
			fields = append(fields, &e.ServedModelTag, &e.ModelFamily)
			for i := range e.AdvertisedCapabilities {
				fields = append(fields, &e.AdvertisedCapabilities[i])
			}
		}
		return append(fields, &p.ErrorMessage, &p.ErrorType), egressContent, nil
	case *operatorv1.OllamaModelResidencyResult:
		fields = nil
		for _, m := range p.Models {
			fields = append(fields, &m.Name)
		}
		return append(fields, &p.ErrorMessage, &p.ErrorType), egressContent, nil
	case *operatorv1.OllamaModelReleaseResult:
		return []*string{&p.ErrorMessage, &p.ErrorType}, egressContent, nil
	case *operatorv1.FetchLogsResult:
		return []*string{&p.Command, &p.Stdout, &p.Stderr, &p.Error}, egressContent, nil
	case *operatorv1.FetchFileDiffResult:
		for _, d := range p.Diffs {
			fields = appendFileDiffFields(fields, d)
		}
		fields = appendFileDiffFields(fields, p.Diff)
		return append(fields, &p.Error), egressContent, nil
	case *operatorv1.FetchHistoryResult:
		if p.WebSession != nil {
			fields = append(fields, &p.WebSession.Title, &p.WebSession.UserIdentity)
		}
		for _, ev := range p.Events {
			fields = append(fields, &ev.ContentText, &ev.CommandRaw, &ev.CommandStdout, &ev.CommandStderr)
			for _, m := range ev.FileMutations {
				fields = append(fields, &m.Filepath, &m.DiffStat)
			}
		}
		return append(fields, &p.Error), egressContent, nil
	case *operatorv1.FetchFileHistoryResult:
		fields = []*string{&p.FilePath}
		for _, h := range p.History {
			fields = append(fields, &h.Message)
		}
		return append(fields, &p.Error), egressContent, nil
	case *operatorv1.RestoreFileResult:
		return []*string{&p.FilePath, &p.Error}, egressContent, nil
	case *operatorv1.InferenceProgressEvent:
		// Scrubbed at the source: the Operator inference stream buffers deltas and scrubs each
		// emitted segment before it is published, so the progress parts already equal the scrubbed
		// terminal parts that OutputHash and ReconcileInferenceProgress verify. Scrubbing again here
		// would break that hash binding. The egress choke point therefore passes them through, and
		// the source fails closed when no scrubber is active.
		return nil, egressDigestBound, nil
	case *operatorv1.ShutdownRequested:
		return []*string{&p.Reason}, egressContent, nil
	case *operatorv1.HeartbeatResult:
		// Operator identity telemetry (hostname, addresses, home and working directories) that the
		// Gateway inventory consumes verbatim. It is not command, output or file content.
		return nil, egressNoContent, nil
	case *operatorv1.InferenceCompletion:
		// Digest-bound: the event receipt signs result_summary = result_digest over the unscrubbed result.
		return nil, egressDigestBound, nil
	case *evalv1.ProviderBoundaryObservationCompleted:
		// Digest-bound observation window: observation_digest covers every field.
		return nil, egressDigestBound, nil
	case *evalv1.ModelProvenanceObservationCompleted:
		// Digest-bound attestation window: attestation_digest covers every field.
		return nil, egressDigestBound, nil
	default:
		return nil, 0, fmt.Errorf("pubsub: payload %T: %w", payload, constants.ErrEgressPayloadUnclassified)
	}
}

func appendFileDiffFields(fields []*string, d *operatorv1.FileDiffEntry) []*string {
	if d == nil {
		return fields
	}
	return append(fields, &d.FilePath, &d.DiffStat, &d.DiffContent)
}

// egressFailureEvent returns the typed failure event published when a content-bearing result cannot
// be egressed. The outcome event decides it first: a cancellation result is a CommandResult too, so
// its payload type alone cannot select Cancel.Failed. It returns an empty EventType when no typed
// failure exists for the outcome and payload.
func egressFailureEvent(outcome constants.EventType, payload proto.Message) constants.EventType {
	if outcome == constants.Event.Operator.Command.Cancelled {
		return constants.Event.Operator.Command.CancelFailed
	}
	switch payload.(type) {
	case *operatorv1.CommandResult:
		return constants.Event.Operator.Command.Failed
	case *operatorv1.FetchLogsResult:
		return constants.Event.Operator.FetchLogs.Failed
	case *operatorv1.FetchFileDiffResult:
		return constants.Event.Operator.FetchFileDiff.Failed
	case *operatorv1.FetchHistoryResult:
		return constants.Event.Operator.FetchHistory.Failed
	case *operatorv1.FetchFileHistoryResult:
		return constants.Event.Operator.FetchFileHistory.Failed
	case *operatorv1.RestoreFileResult:
		return constants.Event.Operator.RestoreFile.Failed
	case *operatorv1.FsReadResult:
		return constants.Event.Operator.FsRead.Failed
	case *operatorv1.FsListResult:
		return constants.Event.Operator.FsList.Failed
	case *operatorv1.FsGrepResult:
		return constants.Event.Operator.FsGrep.Failed
	case *operatorv1.FileEditResult:
		return constants.Event.Operator.FileEdit.Failed
	case *operatorv1.PortCheckResult:
		return constants.Event.Operator.PortCheck.Failed
	case *operatorv1.OllamaModelInventoryResult:
		return constants.Event.Operator.OllamaModelInventory.Failed
	case *operatorv1.OllamaModelResidencyResult:
		return constants.Event.Operator.OllamaModelResidency.Failed
	case *operatorv1.OllamaModelReleaseResult:
		return constants.Event.Operator.OllamaModelRelease.Failed
	default:
		return ""
	}
}

// isEgressWithheld reports whether err means the payload was withheld at the egress choke point.
func isEgressWithheld(err error) bool {
	return errors.Is(err, constants.ErrEgressScrubberUnavailable) || errors.Is(err, constants.ErrEgressPayloadUnclassified)
}

// egressWithheldText returns the fixed, content-free text that replaces a withheld payload's
// error. It is never derived from the payload.
func egressWithheldText(err error) string {
	if errors.Is(err, constants.ErrEgressPayloadUnclassified) {
		return constants.ErrEgressPayloadUnclassified.Error()
	}
	return constants.ErrEgressScrubberUnavailable.Error()
}

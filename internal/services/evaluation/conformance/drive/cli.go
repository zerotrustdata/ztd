// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package drive

import (
	"context"
	"fmt"

	"github.com/zerotrustdata/ztd/internal/cli/operator"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// CLI submits attempts through `ztd operator run`, using the same RunOnce the
// command uses. It drives the outbound executor only (CLI to outbound).
type CLI struct {
	poster       operator.CommandPoster
	target       operator.Target
	cliSessionID string
}

// NewCLI requires a poster, an outbound target session and a CLI session.
func NewCLI(poster operator.CommandPoster, outbound operator.Target, cliSessionID string) (*CLI, error) {
	if poster == nil {
		return nil, fmt.Errorf("cli: poster: %w", constants.ErrMissingRequiredField)
	}
	if outbound.OperatorSessionID == "" {
		return nil, fmt.Errorf("cli: outbound target session: %w", constants.ErrMissingRequiredField)
	}
	if cliSessionID == "" {
		return nil, fmt.Errorf("cli: cli session: %w", constants.ErrMissingRequiredField)
	}
	return &CLI{poster: poster, target: outbound, cliSessionID: cliSessionID}, nil
}

// Drive runs one command through RunOnce and maps its answer to one observation.
// Like governed dispatch, RunOnce blocks under ratify and notary until a human
// decides, so the poster's timeout must outlast the approval wait.
func (c *CLI) Drive(ctx context.Context, a cases.Attempt) (cases.Driven, error) {
	if a.Case.Ingress != cases.IngressCLI {
		return cases.Driven{}, fmt.Errorf("cli %s: ingress %s: %w", a.Case.Fault, a.Case.Ingress, constants.ErrConformanceIngressUnsupported)
	}
	var command string
	switch a.Case.Fault {
	case cases.FaultDoctrine:
		command = a.Case.Command
	case cases.FaultNone, cases.FaultAttendedApprove, cases.FaultAttendedDeny:
		command = cases.MarkerCommand(a)
	default:
		return cases.Driven{}, fmt.Errorf("cli %s: %w", a.Case.Fault, constants.ErrConformanceIngressUnsupported)
	}

	r := operator.RunOnce(c.poster, c.target, command, a.AttemptID, c.cliSessionID)
	if r.TransportErr != nil {
		return cases.Driven{}, fmt.Errorf("cli %s: %w", a.Case.Fault, r.TransportErr)
	}
	return cases.Driven{Observations: []*evalv1.ConformanceObservation{cliObservation(r)}}, nil
}

// cliObservation maps one RunOnce answer to an observation using typed fields
// only. The CLI answer has no HTTP status, so http_status stays 0. A result
// with no typed field is UNTYPED, and its error text is kept as the body.
func cliObservation(r operator.RunResult) *evalv1.ConformanceObservation {
	switch {
	case r.Receipt != nil:
		return &evalv1.ConformanceObservation{Kind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT, Receipt: r.Receipt}
	case r.Rejection != nil:
		return &evalv1.ConformanceObservation{Kind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REJECTION, Rejection: r.Rejection}
	case r.Refusal != nil:
		return &evalv1.ConformanceObservation{Kind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REFUSAL, Refusal: r.Refusal}
	default:
		return &evalv1.ConformanceObservation{Kind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_UNTYPED, UntypedBody: []byte(r.Error)}
	}
}

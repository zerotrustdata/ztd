// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package cases

import (
	"context"
	"fmt"
	"strings"

	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const (
	SuiteID      = "governance-conformance"
	SuiteVersion = "1.0.0"
)

// Ingress names the path a case enters the platform through.
type Ingress string

const (
	IngressDirect           Ingress = "DIRECT"
	IngressDispatchEmbedded Ingress = "DISPATCH_EMBEDDED"
	IngressDispatchOutbound Ingress = "DISPATCH_OUTBOUND"
	IngressMCP              Ingress = "MCP"
	IngressA2A              Ingress = "A2A"
	IngressCLI              Ingress = "CLI"
)

// Executor is the runtime whose Operator executes (and signs) a case.
type Executor string

const (
	ExecutorEmbedded Executor = "embedded"
	ExecutorOutbound Executor = "outbound"
)

// Executor: DIRECT, DISPATCH_EMBEDDED, MCP, A2A -> embedded; DISPATCH_OUTBOUND, CLI -> outbound.
func (i Ingress) Executor() Executor {
	switch i {
	case IngressDispatchOutbound, IngressCLI:
		return ExecutorOutbound
	default:
		return ExecutorEmbedded
	}
}

// UnavailableObservation names the missing optional setup for this ingress.
// Other ingresses must always run; their absence is a configuration error.
func (i Ingress) UnavailableObservation() evalv1.ConformanceObservationKind {
	switch i {
	case IngressDispatchOutbound, IngressCLI:
		return evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_NOT_RUN_OUTBOUND
	case IngressA2A:
		return evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_NOT_RUN_A2A
	default:
		return evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_UNSPECIFIED
	}
}

// Action is the operation a case submits.
type Action string

const (
	ActionMutation Action = "mutation" // writes the case target file
	ActionRead     Action = "read"     // FS_READ of the canary read file
)

// Fault names the single injected defect (or baseline) a case exercises.
type Fault string

const (
	FaultNone                 Fault = "none"
	FaultReadBaseline         Fault = "read_baseline"
	FaultPayloadTampered      Fault = "payload_tampered"
	FaultIDMismatch           Fault = "id_mismatch"
	FaultReplay               Fault = "replay"
	FaultExpired              Fault = "expired"
	FaultProtocolVersion      Fault = "protocol_version"
	FaultEventActionMismatch  Fault = "event_action_mismatch"
	FaultWrongTarget          Fault = "wrong_target"
	FaultStaleStateRoot       Fault = "stale_state_root"
	FaultCallerPosture        Fault = "caller_posture"
	FaultRequestorMismatch    Fault = "requestor_mismatch"
	FaultDoctrine             Fault = "doctrine"
	FaultUngovernedCollection Fault = "ungoverned_collection"
	FaultUnknownSession       Fault = "unknown_session"
	FaultMalformedJSON        Fault = "malformed_json"
	FaultUnknownEventType     Fault = "unknown_event_type"
	FaultMissingField         Fault = "missing_field"
	FaultL2Missing            Fault = "l2_missing"
	FaultL2BelowQuorum        Fault = "l2_below_quorum"
	FaultL2DuplicateSigner    Fault = "l2_duplicate_signer"
	FaultL2SignatureFlipped   Fault = "l2_signature_flipped"
	FaultL2NonMember          Fault = "l2_non_member"
	FaultL2UnknownSet         Fault = "l2_unknown_set"
	FaultL3Missing            Fault = "l3_missing"
	FaultL3Invalid            Fault = "l3_invalid"
	FaultL3Garbage            Fault = "l3_garbage"
	FaultUnattendedMutation   Fault = "unattended_mutation"
	FaultAttendedApprove      Fault = "attended_approve"
	FaultAttendedDeny         Fault = "attended_deny"
	FaultL3ViaArguments       Fault = "l3_via_arguments"
)

// ExpectKind is the shape of one submission's outcome.
type ExpectKind string

const (
	ExpectExecuted  ExpectKind = "EXECUTED"
	ExpectRejected  ExpectKind = "REJECTED"
	ExpectRefused   ExpectKind = "REFUSED_PRE_ENVELOPE"
	ExpectSuspended ExpectKind = "SUSPENDED"
)

// Effect is what the canary (and the A2A stub) must show after the case.
type Effect string

const (
	EffectNone         Effect = "none"          // canary byte-identical, no stub call
	EffectTargetMarker Effect = "target_marker" // canary delta is exactly the target file holding the marker
	EffectA2AStubCall  Effect = "a2a_stub_call" // canary byte-identical, exactly one stub call carrying the marker
)

// Expect is the one exact outcome a submission must produce.
type Expect struct {
	Kind          ExpectKind
	RejectionCode operatorv1.GovernanceRejectionCode // REJECTED only
	RefusalCode   operatorv1.IngressRefusalCode      // REFUSED_PRE_ENVELOPE only
	L2Status      operatorv1.L2Status                // EXECUTED only
	L3Status      operatorv1.L3Status                // EXECUTED only
	Effect        Effect
}

// Case is one row-derived conformance case: one ingress, one fault, one posture.
type Case struct {
	ID            string
	Row           int // the plan's catalogue row
	Ingress       Ingress
	Fault         Fault
	Action        Action
	Posture       evalv1.EvaluationGovernancePosture
	Expect        []Expect // one per submission; two only for FaultReplay
	Attended      bool
	CorpusEntryID string // doctrine rows only
	Command       string // doctrine rows only: the corpus command
}

// Gap records an (ingress, fault) cell the ingress cannot express, and why.
type Gap struct {
	Row     int
	Ingress Ingress
	Fault   Fault
	Reason  string
}

// Attempt is one drive of one case.
type Attempt struct {
	RunID, AttemptID string
	Case             Case
	TargetAbs        string // absolute path of the case target file on the executor
	TargetRel        string // the same file, relative to the canary root
	ReadAbs          string // absolute path of the canary read file on the executor
	Marker           string // exact bytes the mutation writes
}

// Driven is what one Drive returned, in submission order. HTTP exchanges are
// recorded by the runner (T20), not by drivers.
type Driven struct {
	Observations []*evalv1.ConformanceObservation
	A2ACalls     []*evalv1.ConformanceA2ACall
}

// Driver submits one attempt through one ingress. A transport failure is an
// error; every answer the ingress gave is an observation.
type Driver interface {
	Drive(ctx context.Context, a Attempt) (Driven, error)
}

// Sentinel executes one harness-owned FS_READ of readAbs (the canary read file,
// absolute on ex) and returns its signed receipt.
type Sentinel interface {
	Read(ctx context.Context, ex Executor, readAbs string) (*operatorv1.ActionReceipt, error)
}

// CommandSkillPayload is the payload for a skill that carries a shell command.
type CommandSkillPayload struct {
	Cmd string `json:"cmd"`
}

// MarkerSkillPayload is the conformance skill payload that names the marker the
// A2A downstream records.
type MarkerSkillPayload struct {
	Marker string `json:"marker"`
}

// Marker returns the exact bytes a mutation attempt writes.
func Marker(runID, attemptID string) string {
	return fmt.Sprintf("ztd-conformance %s %s\n", runID, attemptID)
}

// MarkerCommand is the one shell command that writes a.Marker to a.TargetAbs.
// The CLI driver (T16) sends it as EXECUTE_BASH. (MCP run_shell_command execs
// argv without a shell, so T17 uses MarkerArgv instead.)
// Shape: printf '%s' '<marker>' > '<targetAbs>' with POSIX single-quote
// escaping of both values.
func MarkerCommand(a Attempt) string {
	return "printf '%s' " + shellQuote(a.Marker) + " > " + shellQuote(a.TargetAbs)
}

// MarkerArgv is the same write as an argv with no shell parsing of the values:
// ["sh", "-c", `printf '%s' "$1" > "$2"`, "sh", marker, targetAbs]. T17 sends
// argv[0] as run_shell_command's command and the rest as its args.
func MarkerArgv(a Attempt) []string {
	return []string{"sh", "-c", `printf '%s' "$1" > "$2"`, "sh", a.Marker, a.TargetAbs}
}

// shellQuote wraps s in single quotes, closing and reopening them around each
// embedded single quote so the shell reads s back verbatim.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

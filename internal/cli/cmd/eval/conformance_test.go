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
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	cliop "github.com/zerotrustdata/ztd/internal/cli/operator"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/drive"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/run"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/testutil"
	harnessclient "github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	harnessconfig "github.com/zerotrustdata/ztd/internal/tools/agent_harness/config"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const (
	conformanceTestRunID           = "run-1"
	conformanceTestOperatorID      = "outbound-operator"
	conformanceTestEmbeddedSession = "embedded-session"
	conformanceTestOutboundSession = "outbound-session"
	conformanceTestUserID          = "user-1"
	conformanceTestCLISession      = "cli-session-1"
)

// conformanceTestOperators is the Operator inventory of a healthy Gateway: the
// embedded Operator and one active outbound session.
func conformanceTestOperators() []*operatorv1.OperatorDocument {
	return []*operatorv1.OperatorDocument{
		{Id: string(constants.DocIDEmbeddedOperator), OperatorSessionId: conformanceTestEmbeddedSession},
		{Id: conformanceTestOperatorID, OperatorSessionId: conformanceTestOutboundSession, Status: string(constants.OperatorStatusActive)},
	}
}

// fakeConformancePoster is the CLI driver's poster. The tests here never post.
type fakeConformancePoster struct{}

func (fakeConformancePoster) Post(string, any) ([]byte, error) {
	return nil, errors.New("conformance test: poster is not used")
}

// rigOptions tunes one conformance command under test.
type rigOptions struct {
	posture   string
	operators []*operatorv1.OperatorDocument
	run       func(context.Context, run.Config) (*evalv1.ConformanceRunRecord, error)
	// wrapGateway wraps the Gateway's runtime file service, e.g. to corrupt reads.
	wrapGateway func(fs.RuntimeFileService) fs.RuntimeFileService
	// serveGateway starts the Gateway the rig's client dials and returns its base URL.
	// Nil leaves the rig without a Gateway, so a test that never dials opens no socket.
	serveGateway func(t *testing.T, posture string, operators []*operatorv1.OperatorDocument) string
}

// conformanceRig is one conformance command: a Gateway served by httptest, two
// temporary runtime roots, and fakes for the run and the CLI poster.
type conformanceRig struct {
	gwRoot string
	opRoot string
	deps   nativeEvalDeps
}

// newConformanceRig builds the deps the command reads. The run defaults to a
// fake that returns an empty record for the id it was given.
func newConformanceRig(t *testing.T, opts rigOptions) conformanceRig {
	t.Helper()
	if opts.posture == "" {
		opts.posture = constants.PostureDoctrine
	}
	if opts.operators == nil {
		opts.operators = conformanceTestOperators()
	}
	if opts.run == nil {
		opts.run = func(_ context.Context, cfg run.Config) (*evalv1.ConformanceRunRecord, error) {
			return &evalv1.ConformanceRunRecord{RunId: cfg.RunID, Posture: evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE, Attended: cfg.Attended}, nil
		}
	}
	gatewayURL := ""
	if opts.serveGateway != nil {
		gatewayURL = opts.serveGateway(t, opts.posture, opts.operators)
	}
	rig := conformanceRig{gwRoot: testutil.TempDir(t), opRoot: testutil.TempDir(t)}
	gwRoot := rig.gwRoot
	rig.deps = nativeEvalDeps{
		configLoader: func(string) (*config.Config, error) { return &config.Config{ProjectRoot: gwRoot}, nil },
		fileSvcFactory: func(root string, _ *slog.Logger) (fs.RuntimeFileService, error) {
			files, err := fs.NewRuntimeFileService(root, testutil.NewTestLogger())
			if err != nil {
				return nil, err
			}
			if root == gwRoot && opts.wrapGateway != nil {
				return opts.wrapGateway(files), nil
			}
			return files, nil
		},
		createRuntimeTree: func(ctx context.Context, files fs.RuntimeFileService) error { return files.CreateRuntimeTree(ctx) },
		clientFactory: func(cfg harnessconfig.Config) (*harnessclient.Client, error) {
			cfg.MTLSBaseURL = gatewayURL
			cfg.PublicBaseURL = gatewayURL
			return harnessclient.New(cfg)
		},
		authLoader: func(fs.RuntimeFileService, *config.Config) (*auth.ClientAuthContext, error) {
			return &auth.ClientAuthContext{UserID: conformanceTestUserID, CLISessionID: conformanceTestCLISession}, nil
		},
		conformance: conformanceDeps{
			run:    opts.run,
			listen: drive.StartA2AStub,
			poster: func(fs.RuntimeFileService, *config.Config) (cliop.CommandPoster, error) {
				return fakeConformancePoster{}, nil
			},
		},
		now:   time.Now,
		newID: func() (string, error) { return conformanceTestRunID, nil },
	}
	return rig
}

// execute runs the command tree with args and returns its combined output.
func (r conformanceRig) execute(t *testing.T, args ...string) (string, error) {
	t.Helper()
	command := evalCmdWithConfig(r.deps)
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&out)
	command.SetArgs(args)
	err := command.Execute()
	return out.String(), err
}

// runArgs is a complete `conformance run` invocation for this rig.
func (r conformanceRig) runArgs(extra ...string) []string {
	args := []string{"conformance", "run", "--a2a-stub-listen", "127.0.0.1:0", "--operator-id", conformanceTestOperatorID, "--operator-root", r.opRoot}
	return append(args, extra...)
}

func TestConformancePosture_MapsEachGatewayPostureAndRejectsAnyOtherName(t *testing.T) {
	tests := []struct {
		name    string
		posture string
		want    evalv1.EvaluationGovernancePosture
		wantErr bool
	}{
		{name: "doctrine", posture: constants.PostureDoctrine, want: evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE},
		{name: "consensus", posture: constants.PostureConsensus, want: evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_CONSENSUS},
		{name: "ratify", posture: constants.PostureRatify, want: evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_RATIFY},
		{name: "notary", posture: constants.PostureNotary, want: evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_NOTARY},
		{name: "empty", posture: "", wantErr: true},
		{name: "unknown", posture: "bogus", wantErr: true},
		{name: "upper case", posture: "DOCTRINE", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := conformancePosture(tt.posture)
			if tt.wantErr {
				require.ErrorIs(t, err, constants.ErrConformancePostureUnknown)
				assert.Equal(t, evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_UNSPECIFIED, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestConformanceRun_RequiresOutboundStubAndApproverBeforeDialing(t *testing.T) {
	tests := []struct {
		name string
		args func(rig conformanceRig) []string
	}{
		{name: "no operator id", args: func(r conformanceRig) []string {
			return []string{"conformance", "run", "--operator-root", r.opRoot, "--a2a-stub-listen", "127.0.0.1:0"}
		}},
		{name: "no operator root", args: func(conformanceRig) []string {
			return []string{"conformance", "run", "--operator-id", conformanceTestOperatorID, "--a2a-stub-listen", "127.0.0.1:0"}
		}},
		{name: "no a2a stub listen", args: func(r conformanceRig) []string {
			return []string{"conformance", "run", "--operator-id", conformanceTestOperatorID, "--operator-root", r.opRoot}
		}},
		{name: "attended without approver", args: func(r conformanceRig) []string {
			return r.runArgs("--attended")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rig := newConformanceRig(t, rigOptions{run: func(context.Context, run.Config) (*evalv1.ConformanceRunRecord, error) {
				t.Fatal("run called before the flags were validated")
				return nil, nil
			}})
			rig.deps.clientFactory = func(harnessconfig.Config) (*harnessclient.Client, error) {
				t.Fatal("client factory called before the flags were validated")
				return nil, nil
			}
			rig.deps.configLoader = func(string) (*config.Config, error) {
				t.Fatal("config loader called before the flags were validated")
				return nil, nil
			}
			_, err := rig.execute(t, tt.args(rig)...)
			assert.ErrorIs(t, err, constants.ErrMissingRequiredField)
		})
	}
}

func TestConformanceReport_RejectsMoreThanFourRunsBeforeTouchingState(t *testing.T) {
	rig := newConformanceRig(t, rigOptions{})
	rig.deps.configLoader = func(string) (*config.Config, error) {
		t.Fatal("config loader called before the run count was checked")
		return nil, nil
	}
	args := []string{"conformance", "report"}
	for _, id := range []string{"run-a", "run-b", "run-c", "run-d", "run-e"} {
		args = append(args, "--run", id)
	}
	_, err := rig.execute(t, args...)
	assert.ErrorIs(t, err, constants.ErrEvaluationFlagsInvalid)
}

func TestConformanceVerdictError_PassesOnlyPassOrUnattendedIncomplete(t *testing.T) {
	tests := []struct {
		name     string
		verdict  evalv1.ConformanceSuiteVerdict
		attended bool
		wantErr  bool
	}{
		{name: "pass", verdict: evalv1.ConformanceSuiteVerdict_CONFORMANCE_SUITE_VERDICT_PASS},
		{name: "incomplete unattended", verdict: evalv1.ConformanceSuiteVerdict_CONFORMANCE_SUITE_VERDICT_INCOMPLETE},
		{name: "incomplete attended", verdict: evalv1.ConformanceSuiteVerdict_CONFORMANCE_SUITE_VERDICT_INCOMPLETE, attended: true, wantErr: true},
		{name: "fail", verdict: evalv1.ConformanceSuiteVerdict_CONFORMANCE_SUITE_VERDICT_FAIL, wantErr: true},
		{name: "invalid evidence", verdict: evalv1.ConformanceSuiteVerdict_CONFORMANCE_SUITE_VERDICT_INVALID_EVIDENCE, wantErr: true},
		{name: "unspecified", verdict: evalv1.ConformanceSuiteVerdict_CONFORMANCE_SUITE_VERDICT_UNSPECIFIED, wantErr: true},
		{name: "pass attended", verdict: evalv1.ConformanceSuiteVerdict_CONFORMANCE_SUITE_VERDICT_PASS, attended: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := conformanceVerdictError(&evalv1.ConformanceVerdictSet{SuiteVerdict: tt.verdict}, tt.attended)
			if !tt.wantErr {
				assert.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, constants.ErrEvalRunVerificationFailed)
		})
	}
}

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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// assignmentContextObserver observes the CLI's assignment context and delegates
// all execution to the production executor against the real eval stack.
type assignmentContextObserver struct {
	evaluation.CampaignAssignmentExecutor
	observe func(context.Context)
}

func (e assignmentContextObserver) ExecuteAssignment(ctx context.Context, req evaluation.AssignmentExecutionRequest) (*evalv1.EvaluationAssignmentResult, error) {
	e.observe(ctx)
	return e.CampaignAssignmentExecutor.ExecuteAssignment(ctx, req)
}

func TestRunsAssignmentDeadline_RealStack(t *testing.T) {
	for _, verb := range []string{"start", "resume", "execute"} {
		for _, explicit := range []bool{false, true} {
			name := verb + "/default"
			if explicit {
				name = verb + "/explicit"
			}
			t.Run(name, func(t *testing.T) {
				run := newRealStackRun(t, constants.PostureDoctrine, "deadline-campaign", 1, defaultRunControlDeps())
				args := []string{"runs", verb, "deadline-campaign", "--daemon=false", "--publish=false"}
				if verb != "start" {
					prepared, err := runStartFlow(run.command(), run.deps, runStartFlowOptions{CampaignID: "deadline-campaign", RunID: "deadline-run", PrepareOnly: true})
					require.NoError(t, err)
					require.True(t, prepared.Prepared)
					args[2] = "deadline-run"
				}
				if explicit {
					args = append(args, "--assignment-timeout=1h")
				}
				args = append(args, "--project-root", run.root)
				observed := false
				started := time.Now()
				factory := run.deps.agentExecutorFactory
				run.deps.agentExecutorFactory = func(in campaignAgentExecutorInputs) (evaluation.CampaignAssignmentExecutor, error) {
					executor, err := factory(in)
					if err != nil {
						return nil, err
					}
					return assignmentContextObserver{CampaignAssignmentExecutor: executor, observe: func(ctx context.Context) {
						observed = true
						deadline, hasDeadline := ctx.Deadline()
						assert.Equal(t, explicit, hasDeadline)
						if explicit {
							assert.False(t, deadline.Before(started.Add(time.Hour)))
							assert.False(t, deadline.After(time.Now().Add(time.Hour)))
						}
					}}, nil
				}
				cmd := evalCmdWithConfig(run.deps)
				var out bytes.Buffer
				cmd.SetOut(&out)
				cmd.SetErr(&out)
				cmd.SetArgs(args)
				ctx, cancel := context.WithCancel(testVersionContext())
				t.Cleanup(cancel)
				require.NoError(t, cmd.ExecuteContext(ctx), out.String())
				assert.True(t, observed, "the CLI must execute a real assignment")
				assert.Equal(t, 1, run.stack.Provider.ChatCount())
			})
		}
	}
}

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package operatorcmd

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"

	"github.com/spf13/cobra"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/operatordeploy"
)

func localOperatorMatches(p operatordeploy.LocalOperatorProcess, op *operatorv1.OperatorDocument) bool {
	if p.SessionID != "" {
		return p.SessionID == op.OperatorSessionId
	}
	hostname, err := os.Hostname()
	return err == nil && operatorHostnameValue(op) == hostname && op.LocalDir != "" && filepath.IsAbs(op.LocalDir) && filepath.Clean(op.LocalDir) == p.Dir
}

type operatorStopResult struct {
	models.StopOperatorResponse
	PID           int    `json:"pid"`
	Method        string `json:"method"`
	Error         string `json:"error,omitempty"`
	GovernedError string `json:"governed_error,omitempty"`
}

func stopLocalOperator(cmd *cobra.Command, p operatordeploy.LocalOperatorProcess, response models.StopOperatorResponse, governedErr error, grace time.Duration) operatorStopResult {
	result := operatorStopResult{StopOperatorResponse: response, PID: p.PID}
	if governedErr != nil {
		result.GovernedError = governedErr.Error()
	}
	fail := func(err error) operatorStopResult {
		result.Success = false
		result.Method = "failed"
		result.Error = err.Error()
		return result
	}
	// Even an unsuccessful request might have delivered shutdown before the response failed.
	exited, err := p.Wait(grace)
	if err != nil {
		return fail(err)
	}
	if exited {
		result.Success = true
		result.Method = "exited"
		return result
	}
	if err := cmd.Context().Err(); err != nil {
		return fail(err)
	}
	if err := p.Signal(false); err != nil {
		return fail(fmt.Errorf("send TERM: %w", err))
	}
	exited, err = p.Wait(grace)
	if err != nil {
		return fail(err)
	}
	if exited {
		result.Success = true
		result.Method = "TERM"
		return result
	}
	if err := cmd.Context().Err(); err != nil {
		return fail(err)
	}
	if err := p.Signal(true); err != nil {
		return fail(fmt.Errorf("send KILL: %w", err))
	}
	exited, err = p.Wait(2 * time.Second)
	if err != nil {
		return fail(err)
	}
	if !exited {
		return fail(constants.ErrProcessSigKillTimeout)
	}
	result.Success = true
	result.Method = "KILL"
	return result
}

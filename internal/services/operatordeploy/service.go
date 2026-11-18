// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package operatordeploy

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

// LocalTargetOverride redirects a named target host to a local directory and
// optional in-process starter (used by test fixtures without SSH).
type LocalTargetOverride = models.OperatorDeployLocalOverride

// DeployRequest configures staging and optional background startup across one
// or more hosts.
type DeployRequest struct {
	HostList       []string
	Dirs           []string
	RemoteDir      string
	SourceBinary   string
	Port           int
	IdentityFile   string
	Parallel       int
	Stage          StageOptions
	EnsureRecovery func(dir string) error
	LocalOverrides map[string]LocalTargetOverride
	RemoteFactory  RemoteDeployClientFactory
	Output         io.Writer
}

// DeployOutcome holds the staged Operators and failed target identifiers from ExecuteDeploy.
type DeployOutcome struct {
	Deployed []DeployedOperator
	Failed   []string
}

// ExecuteDeploy stages the Operator binary across every host in req.HostList,
// runs optional preflight checks, and launches workers when req.Stage.Background
// is true. The caller must invoke the returned cleanup function when finished
// inspecting deployment state.
func ExecuteDeploy(ctx context.Context, req DeployRequest) (DeployOutcome, func(), error) {
	out := req.Output
	if out == nil {
		out = io.Discard
	}
	rFactory := req.RemoteFactory
	if rFactory == nil {
		rFactory = DefaultRemoteDeployClientFactory
	}
	parallel := req.Parallel
	if parallel <= 0 {
		parallel = MaxParallel
	}
	remoteDir := req.RemoteDir
	if remoteDir == "" {
		remoteDir = "~"
	}
	dirs := req.Dirs
	if len(dirs) == 0 {
		dirs = []string{remoteDir}
	}

	var outcome DeployOutcome
	var clients []RemoteDeployClient
	cleanup := func() {
		for _, c := range clients {
			_ = c.Close()
		}
	}
	var err error
	defer func() {
		if err != nil {
			cleanup()
		}
	}()

	for _, rawHost := range req.HostList {
		host := strings.TrimSpace(rawHost)
		override, hasOverride := req.LocalOverrides[host]
		isLocal := IsLocalTarget(host) || hasOverride
		s := HostTransport{
			Host:           host,
			Port:           req.Port,
			IdentityFile:   req.IdentityFile,
			Local:          isLocal,
			EnsureRecovery: req.EnsureRecovery,
			LocalStartFunc: override.StartFunc,
		}
		targetRemoteDir := remoteDir
		targetDirs := dirs
		if hasOverride && override.DestDir != "" {
			targetRemoteDir = override.DestDir
			targetDirs = []string{override.DestDir}
		}

		if isLocal {
			if override.StartFunc == nil {
				sourceBinary := req.SourceBinary
				if sourceBinary == "" {
					sourceBinary, err = os.Executable()
					if err != nil {
						return outcome, cleanup, fmt.Errorf("%w: %w", constants.ErrStatFailed, err)
					}
				}
				cache, prepErr := s.PrepareDir(ctx, strings.TrimSuffix(targetRemoteDir, "/")+"/.deploy-bin")
				if prepErr != nil {
					err = prepErr
					return outcome, cleanup, err
				}
				if instErr := s.InstallBinary(ctx, sourceBinary, cache); instErr != nil {
					err = instErr
					return outcome, cleanup, err
				}
				s.BinaryDir = cache
			} else if req.SourceBinary != "" {
				cache, prepErr := s.PrepareDir(ctx, strings.TrimSuffix(targetRemoteDir, "/")+"/.deploy-bin")
				if prepErr != nil {
					err = prepErr
					return outcome, cleanup, err
				}
				if instErr := s.InstallBinary(ctx, req.SourceBinary, cache); instErr != nil {
					err = instErr
					return outcome, cleanup, err
				}
				s.BinaryDir = cache
			}
			if req.Stage.Preflight != nil {
				if pfErr := s.PreflightGateway(ctx, req.Stage.Preflight); pfErr != nil {
					err = GatewayPreflightError(s.Host, req.Stage.Endpoint, pfErr)
					return outcome, cleanup, err
				}
			}
		} else {
			sourceBinary := req.SourceBinary
			if sourceBinary == "" {
				sourceBinary, err = os.Executable()
				if err != nil {
					return outcome, cleanup, fmt.Errorf("%w: %w", constants.ErrStatFailed, err)
				}
			}
			rc, dialErr := rFactory(ctx, host, req.Port, req.IdentityFile)
			if dialErr != nil {
				err = fmt.Errorf("connect to remote host %s: %w", host, dialErr)
				return outcome, cleanup, err
			}
			clients = append(clients, rc)
			s.RemoteClient = rc

			cache, upErr := rc.UploadBinary(ctx, sourceBinary, targetRemoteDir)
			if upErr != nil {
				err = fmt.Errorf("upload binary to %s: %w", host, upErr)
				return outcome, cleanup, err
			}
			s.BinaryDir = cache

			if req.Stage.Preflight != nil {
				if pfErr := s.PreflightGateway(ctx, req.Stage.Preflight); pfErr != nil {
					err = GatewayPreflightError(s.Host, req.Stage.Endpoint, pfErr)
					return outcome, cleanup, err
				}
			}
		}

		// When LocalStartFunc is provided with no SourceBinary (in-process test
		// fixture), link/install is skipped because the test starts the Operator
		// in-process directly against targetDirs.
		results := DeployOperatorBatch(ctx, s, req.SourceBinary, targetDirs, req.Stage, parallel)
		for result := range results {
			_, _ = io.WriteString(out, result.Output)
			if result.Err != nil {
				_, _ = fmt.Fprintf(out, "Failed to deploy %s:%s: %v\n", s.Host, result.Dir, result.Err)
				outcome.Failed = append(outcome.Failed, s.Host+":"+result.Dir)
				continue
			}
			outcome.Deployed = append(outcome.Deployed, result.Op)
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			err = ctxErr
			return outcome, cleanup, err
		}
	}
	return outcome, cleanup, nil
}

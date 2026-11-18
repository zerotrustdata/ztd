// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package operatorcmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/zerotrustdata/ztd/internal/cli/auth"
	authcmd "github.com/zerotrustdata/ztd/internal/cli/cmd/auth"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/netutil"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	"github.com/zerotrustdata/ztd/internal/services/operatordeploy"
)

func operatorDeployCmd() *cobra.Command {
	return operatorDeployCmdWithConfig(shared.LoadConfig, authcmd.DefaultAPIClientFactory, shared.NewFileSvc, connectGatewayDeploymentEvents)
}

func operatorDeployCmdWithConfig(
	configLoader func(string) (*config.Config, error),
	clientFactory authcmd.APIClientFactory,
	fileSvcFactory func(string, *slog.Logger) (fs.RuntimeFileService, error),
	connectEvents deploymentEventsConnector,
	remoteFactory ...operatordeploy.RemoteDeployClientFactory,
) *cobra.Command {
	rFactory := operatordeploy.DefaultRemoteDeployClientFactory
	if len(remoteFactory) > 0 && remoteFactory[0] != nil {
		rFactory = remoteFactory[0]
	}

	var hosts string
	var port int
	var identityFile string
	var background bool
	var remoteDir string
	var count int
	var approve bool
	var local bool
	var startIndex int
	var parallel int
	var operatorEndpoint string
	start := operatorStartCmd()

	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Deploy the operator binary locally or over SSH",
		Long: `Deploy up to 5000 Operators locally (--local) or to each SSH host (--hosts).
Use --dest-dir for the binary and isolated runtime state; --remote-dir remains an alias.
--local alone defaults --dest-dir to the project's .ztd/operators/, one op-NNNNN directory per Operator.
With --count N, directories are op-00001 through op-N. --start-index adds later batches
without replacing earlier Operators. An explicit --start-index always uses numbered directories.
The binary is installed once per host and hard-linked into each directory.
--parallel bounds concurrent deployments (default and maximum: the Gateway live Operator request quota).

--capabilities selects any combination of data (default), provenance, inference, and observer.
Capability flags and capability-specific settings use the same flags as operator start.
Capability IDs default to unique, stable values per host/directory. Flag values support
{name} (directory basename), {dir} (absolute directory), and {host} substitutions.
--background starts Operators; --endpoint or --operator-endpoint is required.
--operator-endpoint selects the Operator-facing Gateway host and defaults to --endpoint.
Operators dialing a non-loopback host need a Gateway started with --listen-host 0.0.0.0.
--approve approves enrollment as
the authenticated owner after every Operator is staged, in waves that keep a bounded number
of Operators between approval and an established command subscription, refilled as Operators
come online, and verifies every Operator has established its command subscription. Without --approve, Operators remain pending. Repeating a deployment replaces only its own Operators.
--approve requires --trust-bundle, the Gateway CA path on each Operator host. Operators then enroll over wss:// on the
Gateway HTTPS port, and deploy approves only requests carrying the keys each Operator recorded.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := configLoader("")
			if err != nil {
				return err
			}

			fileSvc, err := fileSvcFactory("", slog.Default())
			if err != nil {
				return fmt.Errorf("%w: %w", constants.ErrFileServiceInit, err)
			}

			creds, err := auth.LoadCredentials(fileSvc, cfg)
			if err != nil || creds == nil {
				return fmt.Errorf("%w\nEnroll first with:\n./ztd auth enroll user", constants.ErrNotAuthenticated)
			}

			// A local-only deployment without --dest-dir stages its Operators
			// under the project runtime tree, never the user's home directory.
			numberedDirs := cmd.Flags().Changed("start-index") || startIndex != 1
			destDirSet := cmd.Flags().Changed("dest-dir") || cmd.Flags().Changed("remote-dir")
			if local && strings.TrimSpace(hosts) == "" && !destDirSet {
				if err := fileSvc.MkdirAll(cmd.Context(), constants.OperatorsDirname, constants.PermDirPrivate); err != nil {
					return fmt.Errorf("operator deploy: create %s: %w", constants.OperatorsDirname, err)
				}
				remoteDir = fileSvc.Resolve(constants.OperatorsDirname)
				numberedDirs = true
			}

			hosts, workerEndpoint, err := validateOperatorDeployFlags(
				cmd, local, hosts, remoteDir, parallel, count, startIndex, background, operatorEndpoint, approve,
			)
			if err != nil {
				return err
			}

			startArgs, err := operatorDeployStartArgs(cmd)
			if err != nil {
				return err
			}
			var apiClient authcmd.APIClient
			if approve {
				useDeployGatewayPorts(cmd)
				apiClient, err = clientFactory(fileSvc, cfg)
				if err != nil {
					return fmt.Errorf("operator deploy: create API client: %w", err)
				}
			}

			sourceBinary, err := os.Executable()
			if err != nil {
				return fmt.Errorf("%w: %w", constants.ErrStatFailed, err)
			}
			if _, err := os.Stat(sourceBinary); os.IsNotExist(err) {
				return fmt.Errorf("%w: %s", constants.ErrPathNotFound, sourceBinary)
			}

			hostList := strings.Split(hosts, ",")
			dirs := operatordeploy.OperatorDeployDirs(remoteDir, count)
			if numberedDirs {
				dirs = make([]string, count)
				for i := range dirs {
					dirs[i] = fmt.Sprintf("%s/op-%05d", strings.TrimSuffix(remoteDir, "/"), startIndex+i)
				}
			}
			seen := make(map[string]bool)
			for i, host := range hostList {
				host = strings.TrimSpace(host)
				if host == "" || strings.HasPrefix(host, "-") || strings.ContainsAny(host, " \t\r\n") || seen[host] {
					return fmt.Errorf("%w: invalid or duplicate host %q", constants.ErrMissingRequiredField, host)
				}
				seen[host] = true
				hostList[i] = host
			}
			cmd.Printf("Deploying %d operator(s) to %d target(s): %s\n", len(dirs), len(hostList), strings.Join(hostList, ","))

			ctx := cmd.Context()
			stageOpts := operatordeploy.StageOptions{
				Endpoint:         workerEndpoint,
				Background:       background,
				StartArgs:        startArgs,
				ReadFingerprints: approve,
			}
			if background {
				stageOpts.Preflight = operatorDeployPreflightArgs(cmd, workerEndpoint)
				stageOpts.PreflightFunc = operatordeploy.ExecuteDeployHostPreflight
				events, stopEvents, err := connectEvents(ctx, fileSvc, cfg)
				if err != nil {
					return err
				}
				defer stopEvents()
				stageOpts.Events = events
			}

			acceptUnrecoverable, _ := cmd.Flags().GetBool(shared.FlagAcceptUnrecoverable)
			ensureRecovery := func(dir string) error {
				workerFileSvc, err := fs.NewRuntimeFileService(dir, slog.Default())
				if err != nil {
					return fmt.Errorf("%w: %w", constants.ErrFileServiceInit, err)
				}
				return shared.EnsureRecoveryKey(cmd.InOrStdin(), cmd.ErrOrStderr(), workerFileSvc, cfg.Keyring, keystore.RecoveryRoleOperator, acceptUnrecoverable)
			}

			outcome, cleanup, err := operatordeploy.ExecuteDeploy(ctx, operatordeploy.DeployRequest{
				HostList:       hostList,
				Dirs:           dirs,
				RemoteDir:      remoteDir,
				SourceBinary:   sourceBinary,
				Port:           port,
				IdentityFile:   identityFile,
				Parallel:       parallel,
				Stage:          stageOpts,
				EnsureRecovery: ensureRecovery,
				RemoteFactory:  rFactory,
				Output:         cmd.OutOrStdout(),
			})
			if err != nil {
				return err
			}
			defer cleanup()

			if len(outcome.Failed) > 0 {
				return fmt.Errorf("%w: staging failed for %d of %d operators; no approvals submitted: %s", constants.ErrOperatorDeployFailed, len(outcome.Failed), len(hostList)*len(dirs), strings.Join(outcome.Failed, ", "))
			}

			if approve && len(outcome.Deployed) > 0 {
				cmd.Printf("Approving %d operator(s), at most %d coming online at once...\n", len(outcome.Deployed), operatordeploy.ApprovalWindow)
				if err := enrollDeployedOperators(ctx, apiClient, outcome.Deployed, operatordeploy.ApprovalWindow, cmd.OutOrStdout()); err != nil {
					return err
				}
				for _, op := range outcome.Deployed {
					cmd.Printf("  %s:%s  session %s\n", op.Target.Name(), op.Dir, op.SessionID)
				}
			}

			cmd.Println("\nDeployment complete")
			return nil
		},
	}

	cmd.Flags().StringVar(&hosts, "hosts", "", "Comma-separated SSH hosts (required unless --local)")
	cmd.Flags().StringVar(&operatorEndpoint, "operator-endpoint", "", "Gateway address used by deployed Operators (defaults to --endpoint)")
	cmd.Flags().IntVar(&parallel, "parallel", operatordeploy.MaxParallel, fmt.Sprintf("Maximum Operators staged concurrently (1..%d)", operatordeploy.MaxParallel))
	cmd.Flags().IntVarP(&port, "port", "P", 0, "SSH port to connect to on remote hosts")
	cmd.Flags().StringVarP(&identityFile, "identity", "i", "", "SSH identity file (private key)")
	cmd.Flags().BoolVar(&background, "background", false, "Start operator in background after deployment (requires --endpoint)")
	cmd.Flags().StringVar(&remoteDir, "dest-dir", "~", "Destination directory for binary and isolated Operator runtimes")
	cmd.Flags().StringVar(&remoteDir, "remote-dir", "~", "Alias for --dest-dir")
	cmd.MarkFlagsMutuallyExclusive("dest-dir", "remote-dir")
	cmd.Flags().BoolVar(&local, "local", false, "Deploy on this system without SSH (defaults --dest-dir to .ztd/operators/; requires --dest-dir with --hosts)")
	cmd.Flags().IntVar(&startIndex, "start-index", 1, "First numbered operator directory (1..5000)")
	for _, name := range operatorDeployForwardFlags {
		cmd.Flags().AddFlag(start.Flags().Lookup(name))
	}
	cmd.Flags().IntVar(&count, "count", 1, "Operators to deploy per host, each in its own directory under --dest-dir when greater than 1")
	cmd.Flags().BoolVar(&approve, "approve", false, "Approve the fully staged cohort through a readiness-paced window and wait until it is online (requires --background)")

	return cmd
}

// stagedDecisionTargets resolves only this deployment's IDs against one
// pending snapshot taken after staging, keyed by request ID. Each pending
// request must carry the keys its worker recorded; otherwise another process
// announced that launch ID and nothing is approved.
func stagedDecisionTargets(client authcmd.APIClient, ops []operatordeploy.DeployedOperator) (map[string]models.PlatformEnrollmentDecisionTarget, error) {
	ids := make(map[string]models.PlatformEnrollmentCSRFingerprints, len(ops))
	for _, op := range ops {
		ids[op.RequestID] = op.Fingerprints
	}
	targets := make(map[string]models.PlatformEnrollmentDecisionTarget, len(ids))
	if len(ids) == 0 {
		return targets, nil
	}
	body, err := client.Get(constants.APIPaths.AuthPlatformEnrollmentPending)
	if err != nil {
		return nil, fmt.Errorf("operator deploy: fetch staged requests: %w", err)
	}
	var pending models.PlatformEnrollmentPendingResponse
	if err := json.Unmarshal(body, &pending); err != nil {
		return nil, fmt.Errorf("operator deploy: decode staged requests: %w", err)
	}
	for _, target := range pending.Requests {
		if want, ok := ids[target.RequestID]; ok {
			if target.Fingerprints.Operator != want.Operator || target.Fingerprints.CLI != want.CLI {
				return nil, fmt.Errorf("%w: enrollment request %s does not carry the keys the Operator generated; another process announced this launch ID", constants.ErrOperatorDeployFailed, target.RequestID)
			}
			targets[target.RequestID] = models.PlatformEnrollmentDecisionTarget{RequestID: target.RequestID, Fingerprints: target.Fingerprints}
			delete(ids, target.RequestID)
		}
	}
	if len(ids) != 0 {
		return nil, fmt.Errorf("operator deploy: %d staged requests are no longer pending: %w", len(ids), constants.ErrPlatformEnrollmentRequestNotFound)
	}
	return targets, nil
}

// operatorReadiness is one worker's announced command subscription, or the
// reason none was announced.
type operatorReadiness struct {
	index     int
	sessionID string
	err       error
}

// enrollDeployedOperators approves the fully staged cohort in staging order
// through a sliding window and returns once every worker has announced an
// active command subscription. At most window approved workers are awaiting
// readiness at once, and the window is refilled only by readiness events, so
// the Gateway's issuance, bundle delivery, and first-dial work arrives at the
// rate the fleet actually comes online. A refill waits until half the window
// is free (or the rest of the queue fits), which bounds the cohort to about
// 2N/window fingerprint-bound decisions instead of one per worker.
//
// When the Gateway answers a wave with an L3 suspension, the wave is not
// approved: its approval instructions are written to out, and the wave is
// awaited like any other, since its workers can only come online after a human
// approves it. A wave that never comes online fails the deploy at the timeout.
func enrollDeployedOperators(ctx context.Context, client authcmd.APIClient, ops []operatordeploy.DeployedOperator, window int, out io.Writer) error {
	targets, err := stagedDecisionTargets(client, ops)
	if err != nil {
		return err
	}
	onlineTimeout := operatordeploy.OnlineBaseTimeout + time.Duration(len(ops))*operatordeploy.OnlinePerOperator + mcp.ApprovalRequestTTL
	ctx, cancel := context.WithTimeout(ctx, onlineTimeout)
	defer cancel()

	// Buffered for every worker, so no watcher blocks once this returns early.
	ready := make(chan operatorReadiness, len(ops))
	await := func(i int) {
		go func() {
			sessionID, err := ops[i].Watch.AwaitReady(ctx)
			ready <- operatorReadiness{index: i, sessionID: sessionID, err: err}
		}()
	}
	queue := make([]int, len(ops))
	for i := range ops {
		queue[i] = i
	}

	refill := max(window/2, 1)
	approved, inFlight := 0, 0
	for remaining := len(ops); remaining > 0; remaining-- {
		if free := window - inFlight; len(queue) > 0 && (free >= refill || len(queue) <= free) {
			wave := queue[:min(free, len(queue))]
			queue = queue[len(wave):]
			req := models.PlatformEnrollmentBatchDecisionRequest{Decision: models.PlatformEnrollmentDecisionApprove}
			for _, i := range wave {
				req.Requests = append(req.Requests, targets[ops[i].RequestID])
			}
			if _, err := auth.PostPlatformEnrollmentBatchDecision(client, req); err != nil {
				var l3Err *models.PlatformEnrollmentL3RequiredError
				if !errors.As(err, &l3Err) {
					return fmt.Errorf("operator deploy: approve staged cohort (%d of %d approved): %w", approved, len(targets), err)
				}
				if err := auth.WritePlatformEnrollmentSuspension(out, l3Err.Suspension); err != nil {
					return fmt.Errorf("operator deploy: write L3 approval instructions: %w", err)
				}
			} else {
				approved += len(wave)
			}
			inFlight += len(wave)
			for _, i := range wave {
				await(i)
			}
		}

		r := <-ready
		op := &ops[r.index]
		if r.err != nil {
			return fmt.Errorf("%w: %s:%s: %w", constants.ErrOperatorDeployFailed, op.Target.Name(), op.Dir, operatordeploy.ExplainDeployWait(op.Target, op.Dir, r.err))
		}
		inFlight--
		op.SessionID = r.sessionID
		if err := op.Target.MarkReady(ctx, op.Dir); err != nil {
			return err
		}
	}
	return nil
}

// Reuse start's flag definitions so deploy and start cannot drift in types/defaults.
var operatorDeployForwardFlags = []string{
	"capabilities", "inference-ollama-endpoint", "inference-llamacpp-endpoint", "inference-llamacpp-model-root",
	"inference-keep-alive", "allow-hosted-decision", "decision-model", "decision-only",
	"provider-boundary-observer-id", "provenance-operator-id", "model-storage-root", "model-storage-format",
	"gateway-http-port", "gateway-https-port",
	"heartbeat-interval", "log", "trust-bundle",
	shared.FlagAcceptUnrecoverable,
}

// operatorDeployPreflightArgs returns the gateway-preflight arguments that probe
// the same host and ports the workers will dial.
func operatorDeployPreflightArgs(cmd *cobra.Command, endpoint string) []string {
	args := []string{endpoint}
	for _, name := range []string{"gateway-http-port", "gateway-https-port"} {
		if flag := cmd.Flags().Lookup(name); flag.Changed {
			args = append(args, "--"+name+"="+flag.Value.String())
		}
	}
	return args
}

// useDeployGatewayPorts points this CLI's own Gateway calls (cohort approval)
// at the Gateway ports the workers dial. Deploy's SSH --port shadows the root
// -p flag, so --gateway-http-port and --gateway-https-port are the only way to
// name a non-default Gateway port here. An explicit URL endpoint is kept.
func useDeployGatewayPorts(cmd *cobra.Command) {
	endpoint, _ := cmd.Flags().GetString("endpoint")
	host := strings.TrimSpace(endpoint)
	if strings.Contains(host, "://") {
		return
	}
	host, _, err := netutil.SplitEndpoint(host)
	if err != nil {
		return
	}
	if host == "" {
		host = constants.LocalhostHostname
	}
	if flag := cmd.Flags().Lookup("gateway-http-port"); flag.Changed {
		config.SetHTTPEndpointOverride(net.JoinHostPort(host, flag.Value.String()))
	}
	if flag := cmd.Flags().Lookup("gateway-https-port"); flag.Changed {
		config.SetHTTPSEndpointOverride(net.JoinHostPort(host, flag.Value.String()))
	}
}

func operatorDeployStartArgs(cmd *cobra.Command) ([]string, error) {
	var args []string
	for _, name := range operatorDeployForwardFlags {
		flag := cmd.Flags().Lookup(name)
		if flag.Changed {
			args = append(args, "--"+name+"="+flag.Value.String())
		}
	}
	heartbeat, _ := cmd.Flags().GetInt("heartbeat-interval")
	if err := validateHeartbeatInterval(heartbeat); err != nil {
		return nil, err
	}
	for _, name := range []string{"gateway-http-port", "gateway-https-port"} {
		value, _ := cmd.Flags().GetInt(name)
		if err := operatordeploy.ValidateGatewayPort(name, value); err != nil {
			return nil, err
		}
	}
	return args, nil
}

func validateOperatorDeployFlags(
	cmd *cobra.Command,
	local bool,
	hosts string,
	remoteDir string,
	parallel int,
	count int,
	startIndex int,
	background bool,
	operatorEndpoint string,
	approve bool,
) (effectiveHosts string, workerEndpoint string, err error) {
	if !local && strings.TrimSpace(hosts) == "" {
		return "", "", fmt.Errorf("%w: --hosts or --local is required", constants.ErrMissingRequiredField)
	}

	if local && strings.TrimSpace(hosts) != "" && !cmd.Flags().Changed("dest-dir") && !cmd.Flags().Changed("remote-dir") {
		return "", "", fmt.Errorf("%w: --local with --hosts requires --dest-dir", constants.ErrMissingRequiredField)
	}

	seen := make(map[string]bool)
	var targets []string
	if local {
		targets = append(targets, "local")
		seen["local"] = true
	}
	if hosts != "" {
		for _, raw := range strings.Split(hosts, ",") {
			h := strings.TrimSpace(raw)
			if h == "" || strings.HasPrefix(h, "-") || strings.ContainsAny(h, " \t\r\n") || seen[h] {
				return "", "", fmt.Errorf("%w: invalid or duplicate host %q", constants.ErrMissingRequiredField, raw)
			}
			seen[h] = true
			targets = append(targets, h)
		}
	}
	if len(targets) == 0 {
		return "", "", fmt.Errorf("%w: --hosts or --local is required", constants.ErrMissingRequiredField)
	}
	effectiveHosts = strings.Join(targets, ",")

	if remoteDir == "" || strings.ContainsAny(remoteDir, "\x00\r\n") || (!local && (!operatordeploy.RemoteDirPattern.MatchString(remoteDir) || strings.HasPrefix(remoteDir, "-"))) {
		return "", "", fmt.Errorf("%w: --dest-dir %q must match %s", constants.ErrPathValidation, remoteDir, operatordeploy.RemoteDirPattern)
	}
	if parallel < 1 || parallel > operatordeploy.MaxParallel {
		return "", "", fmt.Errorf("%w: --parallel must be between 1 and %d", constants.ErrMissingRequiredField, operatordeploy.MaxParallel)
	}
	if count < 1 || count > 5000 || startIndex < 1 || startIndex > 5000 || count > 5001-startIndex {
		return "", "", fmt.Errorf("%w: --count and --start-index must select operators within 1..5000", constants.ErrMissingRequiredField)
	}

	endpoint, _ := cmd.Flags().GetString("endpoint")
	endpoint = strings.TrimSpace(endpoint)
	workerEndpoint = strings.TrimSpace(operatorEndpoint)

	hasRemote := false
	for _, h := range strings.Split(effectiveHosts, ",") {
		if operatordeploy.IsRemoteTarget(h) {
			hasRemote = true
			break
		}
	}

	if hasRemote {
		if background {
			if workerEndpoint == "" {
				return "", "", fmt.Errorf("%w: %w: --operator-endpoint is required for remote deployment with --background", constants.ErrMissingRequiredField, constants.ErrOperatorEndpointInvalid)
			}
			if netutil.IsLoopbackEndpoint(workerEndpoint) {
				return "", "", fmt.Errorf("%w: remote deployment requires a non-loopback --operator-endpoint (got %q)", constants.ErrOperatorEndpointInvalid, workerEndpoint)
			}
		} else if workerEndpoint != "" && netutil.IsLoopbackEndpoint(workerEndpoint) {
			return "", "", fmt.Errorf("%w: remote deployment requires a non-loopback --operator-endpoint (got %q)", constants.ErrOperatorEndpointInvalid, workerEndpoint)
		}
	} else {
		if workerEndpoint == "" {
			workerEndpoint = endpoint
		}
	}

	if background && workerEndpoint == "" {
		return "", "", fmt.Errorf("%w: --operator-endpoint or --endpoint is required with --background", constants.ErrMissingRequiredField)
	}
	if background && !operatordeploy.EndpointPattern.MatchString(workerEndpoint) {
		return "", "", fmt.Errorf("%w: Operator endpoint %q must match %s", constants.ErrPathValidation, workerEndpoint, operatordeploy.EndpointPattern)
	}
	if approve && !background {
		return "", "", fmt.Errorf("%w: --approve requires --background", constants.ErrMissingRequiredField)
	}
	// Approving by launch ID is safe only when workers authenticate the
	// Gateway before announcing a request (wss:// against the pinned CA).
	if trustBundle, _ := cmd.Flags().GetString("trust-bundle"); approve && strings.TrimSpace(trustBundle) == "" {
		return "", "", fmt.Errorf("%w: --approve requires --trust-bundle (the Gateway CA on each Operator host)", constants.ErrMissingRequiredField)
	}

	return effectiveHosts, workerEndpoint, nil
}

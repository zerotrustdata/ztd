// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package pubsub

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"unicode"

	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/logging"
	"github.com/zerotrustdata/ztd/internal/services/operatordeploy"
	storage "github.com/zerotrustdata/ztd/internal/services/storage"
	"github.com/zerotrustdata/ztd/internal/uuid"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// OperatorDeployService executes governed OPERATOR_DEPLOY actions at L5.
// It stages and starts Operators in the background with deterministic
// deployment IDs, and never approves enrollment.
type OperatorDeployService struct {
	config        *config.Config
	logger        *slog.Logger
	results       ResultsPublisher
	deployConfig  config.OperatorDeployConfig
	remoteFactory operatordeploy.RemoteDeployClientFactory
	eventsStore   *storage.LocalFirstEventsStore
}

// NewOperatorDeployService creates a new OperatorDeployService. Every required
// dependency fails closed at construction.
func NewOperatorDeployService(
	cfg *config.Config,
	logger *slog.Logger,
	results ResultsPublisher,
	deployCfg config.OperatorDeployConfig,
	remoteFactory operatordeploy.RemoteDeployClientFactory,
	eventsStore *storage.LocalFirstEventsStore,
) (*OperatorDeployService, error) {
	if cfg == nil || logger == nil {
		return nil, fmt.Errorf("operator deploy service: %w", constants.ErrMissingRequiredField)
	}
	if results == nil {
		return nil, fmt.Errorf("operator deploy service: %w", constants.ErrPubSubResultsPublisher)
	}
	if err := requireEventsStore("operator deploy service", eventsStore); err != nil {
		return nil, err
	}
	return &OperatorDeployService{
		config:        cfg,
		logger:        logger,
		results:       results,
		deployConfig:  deployCfg,
		remoteFactory: remoteFactory,
		eventsStore:   eventsStore,
	}, nil
}

type resolvedDeploySettings struct {
	endpoint        string
	httpPort        int
	httpsPort       int
	trustBundlePath string
	capabilities    constants.OperatorCapabilities
	remoteDir       string
	sourceBinary    string
	localOverrides  map[string]operatordeploy.LocalTargetOverride
}

func (s *OperatorDeployService) resolveDeploySettings() resolvedDeploySettings {
	cfgDeploy := s.config.OperatorDeploy

	endpoint := firstNonEmpty(s.deployConfig.Endpoint, cfgDeploy.Endpoint, s.config.Endpoint)
	if endpoint == "" {
		if host := s.config.Gateway.ListenHost; host != "" && host != "0.0.0.0" {
			endpoint = host
		} else {
			endpoint = constants.DefaultEndpoint
		}
	}

	httpPort := firstPositive(s.deployConfig.HTTPPort, cfgDeploy.HTTPPort, s.config.ReportedHTTPPort())
	httpsPort := firstPositive(s.deployConfig.HTTPSPort, cfgDeploy.HTTPSPort, s.config.ReportedHTTPSPort())
	trustBundlePath := firstNonEmpty(s.deployConfig.TrustBundlePath, cfgDeploy.TrustBundlePath, s.config.TrustBundlePath)

	capabilities := s.deployConfig.Capabilities
	if len(capabilities) == 0 {
		capabilities = cfgDeploy.Capabilities
	}
	if len(capabilities) == 0 {
		capabilities = constants.OperatorCapabilities{constants.OperatorCapabilityData}
	}

	remoteDir := firstNonEmpty(s.deployConfig.RemoteDir, cfgDeploy.RemoteDir)
	sourceBinary := firstNonEmpty(s.deployConfig.SourceBinary, cfgDeploy.SourceBinary)
	localOverrides := s.deployConfig.LocalOverrides
	if len(localOverrides) == 0 {
		localOverrides = cfgDeploy.LocalOverrides
	}

	return resolvedDeploySettings{
		endpoint:        endpoint,
		httpPort:        httpPort,
		httpsPort:       httpsPort,
		trustBundlePath: trustBundlePath,
		capabilities:    capabilities,
		remoteDir:       remoteDir,
		sourceBinary:    sourceBinary,
		localOverrides:  localOverrides,
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func firstPositive(values ...int) int {
	for _, v := range values {
		if v > 0 {
			return v
		}
	}
	return 0
}

func validateDeployHostnames(hostnames []string) error {
	if len(hostnames) == 0 {
		return fmt.Errorf("%w: hostnames list cannot be empty", constants.ErrMCPValidateHostnamesEmpty)
	}
	seen := make(map[string]struct{}, len(hostnames))
	for _, h := range hostnames {
		if h == "" {
			return fmt.Errorf("%w: empty hostname", constants.ErrMCPValidateHostnameEmpty)
		}
		if strings.ContainsFunc(h, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
			return fmt.Errorf("%w: hostname contains whitespace or control characters", constants.ErrMCPValidateHostnameWhitespace)
		}
		if strings.HasPrefix(h, "-") {
			return fmt.Errorf("%w: hostname must not start with '-'", constants.ErrValidationFailed)
		}
		if strings.Contains(h, "..") {
			return fmt.Errorf("%w: hostname contains parent directory traversal", constants.ErrValidationFailed)
		}
		if strings.ContainsAny(h, ";&|$`()<>\\{}'\"*?!") {
			return fmt.Errorf("%w: hostname contains dangerous character", constants.ErrMCPValidateHostnameDangerousChar)
		}
		if _, dup := seen[h]; dup {
			return fmt.Errorf("%w: duplicate hostname", constants.ErrValidationFailed)
		}
		seen[h] = struct{}{}
	}
	return nil
}

// HandleDeployRequest unmarshals an OperatorDeployRequested payload, stages and
// starts the Operator on each target host in background mode (never approving
// enrollment), publishes OperatorDeployResult, and returns its canonical summary.
func (s *OperatorDeployService) HandleDeployRequest(ctx context.Context, msg *PubSubCommandMessage) (string, error) {
	var req operatorv1.OperatorDeployRequested
	if err := proto.Unmarshal(msg.Payload, &req); err != nil {
		decodeErr := fmt.Errorf("operator deploy: decode request: %w", err)
		pubErr := s.publishFailureResult(ctx, msg, msg.ID, decodeErr)
		return "", errors.Join(decodeErr, pubErr)
	}

	executionID := req.GetExecutionId()
	if executionID == "" {
		executionID = executionIDFromMessage(msg)
	}
	hostnames := req.GetHostnames()

	if err := validateDeployHostnames(hostnames); err != nil {
		pubErr := s.publishFailureResult(ctx, msg, executionID, err)
		return "", errors.Join(fmt.Errorf("operator deploy: %w", err), pubErr)
	}

	settings := s.resolveDeploySettings()
	if !operatordeploy.EndpointPattern.MatchString(settings.endpoint) {
		err := fmt.Errorf("%w: %q", constants.ErrOperatorEndpointInvalid, settings.endpoint)
		pubErr := s.publishFailureResult(ctx, msg, executionID, err)
		return "", errors.Join(err, pubErr)
	}
	if err := operatordeploy.ValidateGatewayPort("gateway-http-port", settings.httpPort); err != nil {
		pubErr := s.publishFailureResult(ctx, msg, executionID, err)
		return "", errors.Join(err, pubErr)
	}
	if err := operatordeploy.ValidateGatewayPort("gateway-https-port", settings.httpsPort); err != nil {
		pubErr := s.publishFailureResult(ctx, msg, executionID, err)
		return "", errors.Join(err, pubErr)
	}

	launchIDs := make([]string, len(hostnames))
	for i := range hostnames {
		launchIDs[i] = operatordeploy.DeploymentIDForExecution(executionID, i)
	}

	var startArgs []string
	if settings.httpPort > 0 {
		startArgs = append(startArgs, fmt.Sprintf("--gateway-http-port=%d", settings.httpPort))
	}
	if settings.httpsPort > 0 {
		startArgs = append(startArgs, fmt.Sprintf("--gateway-https-port=%d", settings.httpsPort))
	}
	if settings.trustBundlePath != "" {
		startArgs = append(startArgs, "--trust-bundle="+settings.trustBundlePath)
	}
	if len(settings.capabilities) > 0 {
		startArgs = append(startArgs, "--capabilities="+settings.capabilities.Canonical().String())
	}

	s.logger.Info("Operator deploy requested", "execution_id", executionID, "host_count", len(hostnames))

	var deployOut bytes.Buffer
	var launchMu sync.Mutex
	launchIdx := 0
	outcome, cleanup, deployErr := operatordeploy.ExecuteDeploy(ctx, operatordeploy.DeployRequest{
		HostList:       hostnames,
		RemoteDir:      settings.remoteDir,
		SourceBinary:   settings.sourceBinary,
		LocalOverrides: settings.localOverrides,
		RemoteFactory:  s.remoteFactory,
		Output:         &deployOut,
		Stage: operatordeploy.StageOptions{
			Background: true,
			Endpoint:   settings.endpoint,
			StartArgs:  startArgs,
			LaunchIDFunc: func() (string, error) {
				launchMu.Lock()
				defer launchMu.Unlock()
				if launchIdx >= len(launchIDs) {
					return uuid.NewString()
				}
				id := launchIDs[launchIdx]
				launchIdx++
				return id, nil
			},
		},
	})
	if cleanup != nil {
		defer cleanup()
	}

	deployedByHost := make(map[string]operatordeploy.DeployedOperator, len(outcome.Deployed))
	for _, op := range outcome.Deployed {
		deployedByHost[op.Target.Name()] = op
	}

	deployments := make([]*operatorv1.OperatorDeployedHost, 0, len(hostnames))
	for i, host := range hostnames {
		if op, ok := deployedByHost[host]; ok {
			deployments = append(deployments, &operatorv1.OperatorDeployedHost{
				Hostname:     host,
				DeploymentId: op.LaunchID,
				Phase:        string(models.OperatorDeploymentPhasePendingApproval),
				RequestId:    op.RequestID,
				WorkingDir:   op.Dir,
				Success:      true,
				Message:      fmt.Sprintf("Operator staged and started on %s (awaiting human enrollment approval)", host),
			})
			continue
		}
		errMsg := constants.ErrOperatorDeployFailed.Error()
		if deployErr != nil {
			errMsg = deployErr.Error()
		}
		deployments = append(deployments, &operatorv1.OperatorDeployedHost{
			Hostname:     host,
			DeploymentId: launchIDs[i],
			Phase:        string(models.OperatorDeploymentPhaseFailed),
			Success:      false,
			Message:      fmt.Sprintf("Operator deploy failed on %s", host),
			Error:        errMsg,
		})
	}

	var execErr error
	switch {
	case deployErr != nil:
		execErr = fmt.Errorf("%w: %w", constants.ErrOperatorDeployFailed, deployErr)
	case len(outcome.Failed) > 0:
		execErr = fmt.Errorf("%w: %s", constants.ErrOperatorDeployFailed, strings.Join(outcome.Failed, ", "))
	}

	status := operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED
	protoResult := &operatorv1.OperatorDeployResult{
		ExecutionId: executionID,
		Status:      status,
		Deployments: deployments,
	}
	if execErr != nil {
		protoResult.Status = operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED
		protoResult.ErrorMessage = execErr.Error()
		protoResult.ErrorType = logging.ErrorKindValue(execErr)
	}

	publishErr := s.results.PublishOperatorDeployResult(ctx, protoResult, msg)
	if err := errors.Join(execErr, publishErr); err != nil {
		s.logger.Warn("Operator deploy failed", "execution_id", executionID, logging.ErrorKindKey, logging.ErrorKindValue(err))
		return "", fmt.Errorf("operator deploy: %w", err)
	}

	return typedResultSummary(protoResult)
}

func (s *OperatorDeployService) publishFailureResult(
	ctx context.Context,
	msg *PubSubCommandMessage,
	executionID string,
	cause error,
) error {
	result := &operatorv1.OperatorDeployResult{
		ExecutionId:  executionID,
		Status:       operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED,
		ErrorMessage: cause.Error(),
		ErrorType:    logging.ErrorKindValue(cause),
	}
	return s.results.PublishOperatorDeployResult(ctx, result, msg)
}

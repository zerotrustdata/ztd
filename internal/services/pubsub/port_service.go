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
	"log/slog"
	"net"
	"time"

	"github.com/zerotrustdata/ztd/internal/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/scrubbing"
	storage "github.com/zerotrustdata/ztd/internal/services/storage"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"google.golang.org/protobuf/proto"
)

// PortService owns port connectivity check handling.
type PortService struct {
	config      *config.Config
	logger      *slog.Logger
	client      PubSubClient
	eventsStore *storage.LocalFirstEventsStore
	scrubbing   *scrubbing.ScrubbingService
}

// NewPortService creates a new PortService. Every dependency is required: the scrubber is the
// egress choke point (nil or disabled fails with constants.ErrEgressScrubberUnavailable) and the
// local-first events store records observed-state content evidence (nil fails with constants.ErrLocalFirstEventsStoreRequired).
func NewPortService(
	cfg *config.Config,
	logger *slog.Logger,
	client PubSubClient,
	scrubbingSvc *scrubbing.ScrubbingService,
	eventsStore *storage.LocalFirstEventsStore,
) (*PortService, error) {
	if cfg == nil || logger == nil || client == nil {
		return nil, fmt.Errorf("port service: %w", constants.ErrMissingRequiredField)
	}
	if err := requireActiveScrubber("port service", scrubbingSvc); err != nil {
		return nil, err
	}
	if err := requireEventsStore("port service", eventsStore); err != nil {
		return nil, err
	}
	return &PortService{
		config:      cfg,
		logger:      logger,
		client:      client,
		eventsStore: eventsStore,
		scrubbing:   scrubbingSvc,
	}, nil
}

// HandlePortCheckRequest processes an inbound port check request.
func (ps *PortService) HandlePortCheckRequest(ctx context.Context, msg *PubSubCommandMessage) (string, error) {
	var protoPort operatorv1.CheckPortRequested
	if err := proto.Unmarshal(msg.Payload, &protoPort); err != nil {
		ps.logger.Error("Failed to decode port check payload as protobuf CheckPortRequested", string(constants.ConnectionStateError), err)
		return "", errors.Join(fmt.Errorf("port check: decode request: %w", err), publishLFEAErrorTo(ctx, ps.client, ps.config, ps.logger, msg, constants.Event.Operator.PortCheck.Failed, "invalid request payload", ps.eventsStore, ps.scrubbing))
	}

	if protoPort.Port <= 0 || protoPort.Port > 65535 {
		ps.logger.Warn("Port check request with invalid port", "port", protoPort.Port)
		return "", errors.Join(fmt.Errorf("port must be between 1 and 65535: %w", constants.ErrValidationFailed), publishLFEAErrorTo(ctx, ps.client, ps.config, ps.logger, msg, constants.Event.Operator.PortCheck.Failed, "port must be between 1 and 65535", ps.eventsStore, ps.scrubbing))
	}

	host := protoPort.Host
	if host == "" {
		host = "localhost"
	}
	protocol := protoPort.Protocol
	if protocol == "" {
		protocol = string(constants.NetworkProtocolTCP)
	}

	executionID := executionIDFromMessage(msg)

	ps.logger.Info("Port check requested (via Protobuf)", "execution_id", executionID, "port", protoPort.Port, "protocol", protocol)

	start := time.Now()
	address := net.JoinHostPort(host, fmt.Sprintf("%d", protoPort.Port))
	conn, dialErr := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, protocol, address)
	latencyMs := time.Since(start).Seconds() * 1000

	entry := &operatorv1.PortCheckEntry{
		Host: host,
		Port: protoPort.Port,
		Open: dialErr == nil,
	}
	if dialErr == nil {
		conn.Close()
		entry.LatencyMs = float32(latencyMs)
	} else {
		entry.Error = dialErr.Error()
	}

	payload := &operatorv1.PortCheckResult{
		ExecutionId: executionID,
		Status:      operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
		Results:     []*operatorv1.PortCheckEntry{entry},
	}
	// A closed port is a completed observation. Caller cancellation is an action failure.
	return "", errors.Join(ctx.Err(), publishLFEATypedResponseTo(ctx, ps.client, ps.config, ps.logger, msg, constants.Event.Operator.PortCheck.Completed, payload, ps.eventsStore, ps.scrubbing))
}

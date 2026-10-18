// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gwremote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/zerotrustdata/ztd/internal/cli/sse"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

// watchEvidenceWindows follows the CLI session's Gateway SSE stream,
// reconnecting with backoff, and reports the provider attempt ID of every
// eventType evidence-recorded event. connected runs on each (re)connect. It
// stops when ctx ends or the Gateway rejects the CLI session.
func watchEvidenceWindows(ctx context.Context, newStream func() *sse.Client, eventType constants.EventType, connected func(), recorded func(string)) error {
	if newStream == nil || connected == nil || recorded == nil {
		return fmt.Errorf("watch %s: %w", eventType, constants.ErrMissingRequiredField)
	}
	stream := newStream()
	stream.SetReconnectOnClose(true)
	stream.SetOnConnect(connected)
	var terminal error
	stream.SetOnDisconnect(func(err error) {
		if errors.Is(err, constants.ErrCLISessionRefreshRequired) {
			terminal = err
		}
	})
	stream.Run(ctx, func(rawType, data string) {
		event, ok := sse.DecodeEvent(rawType, data)
		if !ok || event.Type != string(eventType) {
			return
		}
		var update models.EvidenceWindowRecorded
		if json.Unmarshal(event.Data, &update) != nil || update.ProviderAttemptID == "" {
			return
		}
		recorded(update.ProviderAttemptID)
	})
	if terminal != nil {
		return fmt.Errorf("watch %s: %w", eventType, terminal)
	}
	return nil
}

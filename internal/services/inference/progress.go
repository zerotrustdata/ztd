// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package inference

import (
	"context"

	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

type progressReporterKey struct{}

// ProgressReporter publishes bounded inference progress telemetry while a
// governed provider stream is active. Progress events are delivery
// telemetry only; the signed InferenceCompletion remains authoritative.
type ProgressReporter func(*operatorv1.InferenceProgressEvent) error

// WithProgressReporter attaches a progress reporter to ctx for the duration
// of one governed inference execution.
func WithProgressReporter(ctx context.Context, reporter ProgressReporter) context.Context {
	if reporter == nil {
		return ctx
	}
	return context.WithValue(ctx, progressReporterKey{}, reporter)
}

// ProgressReporterFromContext returns the progress reporter attached to ctx,
// or nil when streaming progress is not requested.
func ProgressReporterFromContext(ctx context.Context) ProgressReporter {
	reporter, _ := ctx.Value(progressReporterKey{}).(ProgressReporter)
	return reporter
}

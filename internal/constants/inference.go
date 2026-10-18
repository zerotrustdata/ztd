// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package constants

import "time"

// EvaluationAppName is the platform-enrolled application identity the eval
// runner presents for governed inference dispatch. It enrolls once through
// `ztd auth enroll app` and never borrows another component's credentials.
const EvaluationAppName = "ztd-eval"

// InferenceDefaultKeepAlive is the Ollama keep-alive `ztd operator start` uses
// when --inference-keep-alive is not given. The Inference Operator configures
// no models: each governed request carries the model the user chose.
const InferenceDefaultKeepAlive = "-1"

const (
	// Bound full blob hashes independently of the command-channel goroutine count.
	ModelProvenanceHashConcurrency = 2
	ModelProvenanceHashBufferBytes = 1 << 20
	// A blob whose identity changed within this window of its hash start is
	// re-hashed on next use: coarse filesystem timestamps cannot order the
	// change against the hash.
	ModelProvenanceBlobIdentitySettleWindow = time.Second
	// Strict verification waits on attestation-recorded events for durable
	// completion coverage, never a fixed sleep or a re-read timer.
	ModelProvenanceCompletionTimeout = 5 * time.Minute
)

// ProviderObservationCompletionTimeout bounds the wait for the Observer's
// observation-recorded event after governed inference returns. FINALIZE only
// stops sampling and signs the window, so a window this late is missing.
const ProviderObservationCompletionTimeout = 30 * time.Second

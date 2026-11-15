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

// InferenceUnloadKeepAlive is the provider keep-alive that unloads a model
// once its request completes. An Ollama request carrying it and no prompt
// releases the model; a llama.cpp router accepts it as unload-after-request.
const InferenceUnloadKeepAlive = "0"

// OllamaDoneReasonUnload is the done_reason Ollama returns when it accepts a
// keep-alive 0 release request.
const OllamaDoneReasonUnload = "unload"

// OllamaDoneReasonLoad is the done_reason Ollama returns when it accepts a
// chat request with no messages, which loads the model and generates nothing.
const OllamaDoneReasonLoad = "load"

// InferenceFinishReasonRepetition is the finish reason of a generation the
// Inference Operator stopped because its output degenerated into a loop. It
// is the model's output, not a provider fault, and is distinct from "length",
// which a generation reaches by filling its loaded context window.
const InferenceFinishReasonRepetition = "repetition"

// Repetition stop. The Inference Operator watches the most recent
// InferenceRepetitionWindowBytes of decoded output (text and thinking, in
// arrival order) and stops the generation once that output ends in one span
// repeated back to back at least InferenceRepetitionMinRepeats times over at
// least InferenceRepetitionMinSpanBytes. The window therefore detects loops
// with a period up to InferenceRepetitionWindowBytes /
// InferenceRepetitionMinRepeats. The tail is re-examined after every
// InferenceRepetitionCheckIntervalBytes of new output. These bound a loop,
// not output length: a generation that never repeats runs to its window.
const (
	InferenceRepetitionWindowBytes        = 16 << 10
	InferenceRepetitionMinSpanBytes       = 4 << 10
	InferenceRepetitionMinRepeats         = 4
	InferenceRepetitionCheckIntervalBytes = 512
)

// ModelPullDefaultTimeout bounds a governed model pull that names no timeout.
// A pull downloads the whole model, so it does not share the provider request
// deadline.
const ModelPullDefaultTimeout = 24 * time.Hour

// Case and investigation IDs that scope the receipts of governed provider
// model-maintenance commands on the Inference Operator. A command dispatched
// for a run carries the run ID as its case instead of the maintenance case.
const (
	ProviderModelMaintenanceCaseID        = "ollama-model-maintenance"
	ProviderModelPullInvestigationID      = "ollama-model-pull"
	ProviderModelCopyInvestigationID      = "ollama-model-copy"
	ProviderModelInventoryInvestigationID = "ollama-model-inventory"
	ProviderModelResidencyInvestigationID = "ollama-model-residency"
	ProviderModelReleaseInvestigationID   = "ollama-model-release"
)

// Inference provider classes. The class names the provider API an Inference
// Operator calls; it is recorded on provider inventory entries and formation
// models. Both are local providers serving weights the deployment holds.
const (
	InferenceProviderOllama   = "ollama"
	InferenceProviderLlamaCpp = "llamacpp"
)

// Model store layouts a Provenance Operator attests beneath its
// --model-storage-root (--model-storage-format).
const (
	// ModelStorageFormatOllama is Ollama's manifests/ + content-addressed blobs/ store.
	ModelStorageFormatOllama = "ollama"
	// ModelStorageFormatLlamaCpp is a llama-server --models-dir of GGUF files.
	ModelStorageFormatLlamaCpp = "llamacpp"
)

// ModelWeightMediaTypeGGUF labels a GGUF weight file attested from a
// llama.cpp model store.
const ModelWeightMediaTypeGGUF = "application/vnd.ggml.gguf"

// ModelWeightFormatGGUF is the provider inventory weight format of a GGUF
// model, the value Ollama reports in its model details.
const ModelWeightFormatGGUF = "gguf"

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

// ProviderObservationCompletionTimeout bounds a strict run's completion wait for
// the Observer's observation-recorded events. Each assignment's windows are
// sealed as its result is recorded, so the wait covers only the run's last
// windows; FINALIZE only stops sampling and signs, so a window this late is
// missing.
const ProviderObservationCompletionTimeout = 30 * time.Second

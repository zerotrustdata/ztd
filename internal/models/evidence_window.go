// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package models

// EvidenceWindowRecorded is the payload of the Gateway's evidence-recorded SSE
// events (model provenance attestation, provider-boundary observation): the
// Gateway persisted the window for one provider attempt. It is an
// invalidation, not evidence: consumers load the window through its read path.
type EvidenceWindowRecorded struct {
	ProviderAttemptID string `json:"provider_attempt_id"`
}

// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// Package entry for ztd-adapter (package.json "main" / "exports" -> dist/index.js).
// This file re-exports only the public surface documented in
// docs/guides/build_observe_frontend.md ("The audited adapter"). Modules under
// src/ remain the single implementation; this barrel adds no behavior. Types
// are re-exported only where they appear in the signatures of the documented
// APIs, so a builder can annotate values it receives from them.

export { parseRuntimeConfig, RuntimeConfigError } from './config/runtime_config.js';
export type { FrontendRuntimeConfig } from './config/runtime_config.js';

export { createCredentialedFetch, GatewayFetchError } from './api/fetch.js';
export type { CredentialedFetch } from './api/fetch.js';

export { EndpointNotAllowedError } from './api/allowlist.js';

export { createObserveClient, ObserveResponseValidationError } from './api/observe_client.js';
export type { ObserveClient } from './api/observe_client.js';

export { registerPasskey, authenticatePasskey, enrollPasskey } from './webauthn/ceremonies.js';
export type { CeremonyResult } from './webauthn/ceremonies.js';

export { SseStream } from './sse/stream.js';
export type {
  SseConnectionState,
  SseStreamCallbacks,
  SseStreamOptions,
  ReconcileReason,
} from './sse/stream.js';

export { normalizeGatewayEvent, NormalizedEventError } from './sse/normalizer.js';
export type { NormalizedGatewayEvent } from './sse/normalizer.js';

export { adapterReducer } from './state/stores.js';
export type { AdapterState, AdapterAction } from './state/stores.js';

// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// Wire shapes consumed by the console. Sources of truth:
//   internal/models/auth.go, internal/models/gateway.go

export interface User {
  id: string;
  status?: string;
  roles?: string[];
}

export interface PasskeyCredential {
  /** Standard base64 (Go []byte JSON encoding). */
  id: string;
  attestation_type?: string;
  created_at_unix_ms?: number;
  last_used_at_unix_ms?: number;
}

export type OperatorStatus =
  | 'active'
  | 'available'
  | 'bound'
  | 'offline'
  | 'stale'
  | 'stopped'
  | 'terminated'
  | 'unavailable';

export type OperatorType = 'embedded' | 'remote';

export type OperatorCapability = 'data' | 'inference' | 'provenance' | 'observer';

export interface Operator {
  id: string;
  user_id: string;
  name?: string;
  status: OperatorStatus;
  operator_type?: OperatorType;
  operator_capabilities?: OperatorCapability[];
  operator_session_id?: string;
  bound_web_session_id?: string;
  current_hostname?: string;
  system_fingerprint?: string;
  last_heartbeat_at?: string;
  /** Set only on an active remote Operator; it reads as stale once this passes (lib/liveness.ts). */
  stale_at?: string;
  started_at?: string;
  created_at: string;
  updated_at: string;
}

export interface BindResponse {
  success: boolean;
  bound_count?: number;
  failed_count?: number;
  unbound_count?: number;
  failed_operator_ids?: string[];
  error?: string;
}

export interface SuspendedTransaction {
  transaction_hash: string;
  tool_name?: string;
  created_at?: string;
  expires_at?: string;
}

export interface PlatformEnrollmentRequest {
  request_id: string;
  component_kind?: string;
  component_name?: string;
  instance_id?: string;
  hostname?: string;
  system_fingerprint?: string;
  state: string;
  created_at?: string;
  expires_at?: string;
  fingerprints?: { app?: string; operator?: string; cli?: string; signing_key?: string };
}

export interface PlatformEnrollmentL3SuspensionResponse {
  status: 'l3_required';
  posture: string;
  tx_hash: string;
  approval_url: string;
  expires_at: string;
  message: string;
}

export type PlatformEnrollmentDecisionResponse =
  | PlatformEnrollmentRequest
  | PlatformEnrollmentL3SuspensionResponse;

export function isPlatformEnrollmentL3Suspension(
  v: unknown,
): v is PlatformEnrollmentL3SuspensionResponse {
  if (!v || typeof v !== 'object') return false;
  const r = v as Record<string, unknown>;
  return r.status === 'l3_required' && typeof r.tx_hash === 'string' && r.tx_hash.length > 0;
}


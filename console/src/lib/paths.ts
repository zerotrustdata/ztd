// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// Browser-reachable Gateway routes. The canonical registry is
// protocol/constants/api_paths.json; values here mirror it, and the Go mirror
// lives in internal/constants/api_paths.go. Every route here is classified
// RouteAuthNone, RouteAuthWebSession, or RouteAuthDual in
// internal/services/gateway/gateway_auth.go.

const enc = encodeURIComponent;

export const Paths = {
  health: '/api/v1/health',
  bootstrapStatus: '/api/v1/auth/bootstrap/status',
  logout: '/api/v1/auth/logout',
  usersMe: '/api/v1/users/me',
  sessionsMe: '/api/v1/auth/sessions/me',

  passkeys: '/api/v1/auth/passkeys',
  passkey: (credentialId: string) => `/api/v1/auth/passkeys/${enc(credentialId)}`,
  consoleRegisterChallenge: '/api/v1/auth/passkeys/console/register/challenge',
  consoleRegisterVerify: '/api/v1/auth/passkeys/console/register/verify',
  consoleAuthenticateChallenge: '/api/v1/auth/passkeys/console/authenticate/challenge',
  consoleAuthenticateVerify: '/api/v1/auth/passkeys/console/authenticate/verify',
  enrollmentRegisterChallenge: '/api/v1/auth/passkeys/enrollment/register/challenge',
  enrollmentRegisterVerify: '/api/v1/auth/passkeys/enrollment/register/verify',

  approvals: '/api/v1/approvals',
  approvalChallenge: (txHash: string) => `/api/v1/approvals/${enc(txHash)}/challenge`,
  approvalVerify: (txHash: string) => `/api/v1/approvals/${enc(txHash)}/verify`,
  approvalsRespond: '/api/v1/approvals/respond',

  cliRecoveryStatus: (token: string) => `/api/v1/auth/cli/recovery/status?token=${enc(token)}`,
  cliRecoveryApprove: '/api/v1/auth/cli/recovery/approve',
  platformEnrollmentsPending: '/api/v1/auth/platform-enrollments/pending',
  platformEnrollmentDecision: '/api/v1/auth/platform-enrollments/decision',

  operators: '/api/v1/operators',
  operator: (operatorId: string) => `/api/v1/operators/${enc(operatorId)}`,
  operatorStop: (operatorId: string) => `/api/v1/operators/${enc(operatorId)}/stop`,
  operatorsBind: '/api/v1/operators/bind',
  operatorsUnbind: '/api/v1/operators/unbind',

  sseStream: '/api/v1/sse/stream',

  // OpenAPI 2.0 spec compiled into the Gateway (internal/services/gateway/docs);
  // classified RouteAuthDual, so the session cookie is accepted.
  apiSpec: '/swagger/doc.json',

  // Served on the Gateway's plain-HTTP bootstrap port, not the HTTPS console port.
  operatorBinary: (filename: string) => `/.well-known/ztd/bin/${filename}`,
  deployScriptLinux: '/ztd-deploy.sh',
  deployScriptWindows: '/ztd-deploy.ps1',
} as const;

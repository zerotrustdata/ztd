// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package constants

// KV key and session type constants.
// Canonical values defined in protocol/constants/kv_keys.json (the source of truth).

const (
	KVCachePrefix = "ztd"
)

// SentinelKeyPrefix namespaces sentinel UEI tokens in kv_store.
const SentinelKeyPrefix = "ztd:sentinel:"

// OperatorHeartbeatKeyPrefix namespaces per-Operator heartbeat telemetry in
// kv_store. The rows are observed-tier, so heartbeats never move the bound root.
const OperatorHeartbeatKeyPrefix = "ztd:operator:heartbeat:"

// ScrubbingTokenKeyPrefix is the key prefix for UEI token persistence in the KV store.
// Combined with a token (e.g. "{{UEI_3f9a0c12be47_1}}") to form the full key passed to TokenStore.
const ScrubbingTokenKeyPrefix = "uei_token_"

// ScrubbingTokenOwnerKey holds the runtime's UEI token owner namespace in the KV
// store. It is outside ScrubbingTokenKeyPrefix and never expires: every token the
// runtime mints names this owner.
const ScrubbingTokenOwnerKey = "uei_owner"

const (
	KVKeySessionWeb                   = "ztd:sessions:{session.type}:{session.id}"
	KVKeySessionOperator              = "ztd:sessions:operator:{operator.session.id}"
	KVKeySessionOperatorBind          = "ztd:sessions:operator:{operator.session.id}:bind"
	KVKeySessionWebBind               = "ztd:sessions:web:{web.session.id}:bind"
	KVKeyOperatorFirstDeployed        = "ztd:operator:{operator.id}:first.deployed"
	KVKeyOperatorTrackedStatus        = "ztd:operator:{operator.id}:tracked.status"
	KVKeyUserOperators                = "ztd:user:{user.id}:operators"
	KVKeyUserWebSessions              = "ztd:user:{user.id}:web_sessions"
	KVKeyUserMemories                 = "ztd:user:{user.id}:memories"
	KVKeyInvestigationAttachment      = "ztd:investigation:{investigation.id}:attachment:{attachment.id}"
	KVKeyInvestigationAttachmentIndex = "ztd:investigation:{investigation.id}:attachment.index"
	KVKeyAuthNonce                    = "ztd:auth:nonce:{nonce}"
	KVKeyAuthTokenDownload            = "ztd:auth:token:download:{token}"            // #nosec G101 - constant string, not credential
	KVKeyAuthTokenDevice              = "ztd:auth:token:device:{token}"              // #nosec G101 - constant string, not credential
	KVKeyAuthTokenDeviceUses          = "ztd:auth:token:device:{token}:uses"         // #nosec G101 - constant string, not credential
	KVKeyAuthTokenDeviceFingerprints  = "ztd:auth:token:device:{token}:fingerprints" // #nosec G101 - constant string, not credential
	KVKeyAuthTokenDeviceRegLock       = "ztd:auth:token:device:{token}:reg.lock"     // #nosec G101 - constant string, not credential
	KVKeyAuthDeviceList               = "ztd:auth:device.list:{user.id}"
	KVKeyAuthLoginFailed              = "ztd:auth:login:{identifier}:failed"
	KVKeyAuthLoginLock                = "ztd:auth:login:{identifier}:lock"
	KVKeyAuthLoginIPAccounts          = "ztd:auth:login:ip:{ip}:accounts"
	KVKeyExecutionPendingCmd          = "ztd:execution:{execution.id}:pending.cmd"
)

const (
	KVSessionTypeWeb      = "web"
	KVSessionTypeOperator = "operator"
)

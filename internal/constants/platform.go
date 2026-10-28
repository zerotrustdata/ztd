// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package constants

import "time"

const (
	InferenceRequestSchemaVersion = "1.0"

	// InferenceMaxOutputTokens is the 16k-token hard upper bound on one inference
	// call's output. It is a ceiling, not a default tuning value: a request
	// above it is rejected, and a request that states no limit (0) runs under it.
	InferenceMaxOutputTokens int32 = 16_384
)

// Platform binary names.
const (
	BinaryNameWindows = "ztd-windows-amd64.exe"
	BinaryNameLinux   = "ztd-linux-amd64"
	BinaryNameDarwin  = "ztd-darwin-amd64"
)

// Supported architectures.
const (
	ArchAMD64 = "amd64"
	ArchARM64 = "arm64"
	Arch386   = "386"
)

// Supported operating systems.
const (
	OSLinux   = "linux"
	OSDarwin  = "darwin"
	OSWindows = "windows"
)

// Log level names.
const (
	LogLevelInfo    = "info"
	LogLevelError   = "error"
	LogLevelDebug   = "debug"
	LogLevelDefault = LogLevelInfo
)

// Master key store approval. When reading the vault master key blocks for
// longer than KeyStoreApprovalNoticeDelay, the OS key store is waiting on the
// user (macOS asks before a rebuilt or upgraded ztd binary reads its Keychain
// item). The keystore then logs KeyStoreAwaitingApprovalLog, and a background
// gw start that finds it in the child's log keeps waiting up to
// KeyStoreApprovalTimeout in addition to its normal startup budget.
const (
	KeyStoreApprovalNoticeDelay = 2 * time.Second
	KeyStoreApprovalTimeout     = 5 * time.Minute
	KeyStoreAwaitingApprovalLog = "[Keystore] Waiting for approval to read the master key from the OS key store"
	// KeyStoreSSHAgentKeyLifetime bounds keys loaded by the attended startup recovery.
	KeyStoreSSHAgentKeyLifetime = "8h"
)

// PublicMirrorVerificationTimeout bounds verification before public content
// can be served. Managed starts allow this budget in addition to health polling.
const PublicMirrorVerificationTimeout = 5 * time.Minute

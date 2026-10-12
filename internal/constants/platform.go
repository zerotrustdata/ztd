// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package constants

import "time"

const (
	EvaluationReceiptPollInterval = 500 * time.Millisecond
	EvaluationReceiptPollTimeout  = 30 * time.Second
	InferenceRequestSchemaVersion = "1.0"

	// InferenceMaxOutputTokens is the hard upper bound on one inference
	// call's output. It is a ceiling, not a default tuning value: a request
	// above it is rejected, and a request that states no limit (0) runs under it.
	InferenceMaxOutputTokens int32 = 256_000
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

// Governance posture names.
const (
	PostureDoctrine  = "doctrine"
	PostureConsensus = "consensus"
	PostureRatify    = "ratify"
	PostureNotary    = "notary"
)

type GovernancePostureRequirements struct {
	RequiresL2 bool
	RequiresL3 bool
}

func GetGovernancePostureRequirements(posture string) (GovernancePostureRequirements, bool) {
	switch posture {
	case PostureDoctrine:
		return GovernancePostureRequirements{}, true
	case PostureConsensus:
		return GovernancePostureRequirements{RequiresL2: true}, true
	case PostureRatify:
		return GovernancePostureRequirements{RequiresL3: true}, true
	case PostureNotary:
		return GovernancePostureRequirements{RequiresL2: true, RequiresL3: true}, true
	default:
		return GovernancePostureRequirements{}, false
	}
}

// Log level names.
const (
	LogLevelInfo    = "info"
	LogLevelError   = "error"
	LogLevelDebug   = "debug"
	LogLevelDefault = LogLevelInfo
)

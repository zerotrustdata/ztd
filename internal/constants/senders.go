// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package constants

// Sender and message type constants.
// Canonical values defined in protocol/constants/senders.json (the source of truth).
// This file is hand-maintained, not generated: constgen does not produce it, so
// keep the constants in step with senders.json by hand.

const (
	SourceUserChat     = "ztd.v1.source.user.chat"
	SourceUserTerminal = "ztd.v1.source.user.terminal"
	SourceSystem       = "ztd.v1.source.system"
)

const (
	MessageTypeText     = "text"
	MessageTypeCode     = "code"
	MessageTypeCall     = "call"
	MessageTypeResult   = "result"
	MessageTypeError    = "error"
	MessageTypeThinking = "thinking"
)

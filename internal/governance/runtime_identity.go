// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package governance

// RuntimeIdentity is the Operator and audit session a runtime owns. The session
// is the one the runtime's local audit store holds; it is never taken from an
// envelope's claim.
type RuntimeIdentity struct {
	OperatorID string
	SessionID  string
}

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import "context"

// testEmbeddedSessionID is the embedded Operator session the fixed resolver
// reports to mcp.NewGatewayService in tests that do not exercise app-originated
// envelopes.
const testEmbeddedSessionID = "test-embedded-operator-session"

// fixedEmbeddedSession satisfies mcp.EmbeddedSessionResolver with a constant
// claimed embedded Operator session.
type fixedEmbeddedSession struct{}

func (fixedEmbeddedSession) OperatorSessionID(context.Context) (string, error) {
	return testEmbeddedSessionID, nil
}

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package tribunal

import (
	"fmt"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// doctrineFeedback screens exactly the typed payload used by dispatch,
// including argv joined for command detection and individual JSON values.
// It reveals only violations of this candidate, never the doctrine inventory.
func doctrineFeedback(doctrine *governance.L1Doctrine, arguments models.RunShellCommandRequest) string {
	violations := doctrine.ValidatePayload(&operatorv1.McpCallRequested{
		ToolName: constants.MCPToolRunShellCommand, ArgumentsJson: arguments.CanonicalJSON(),
	})
	if len(violations) == 0 {
		return ""
	}
	return fmt.Sprintf("L1 doctrine rejected these arguments: %s\nViolations:\n%s\nDerive a different allowed command that satisfies the original request. Do not disguise the blocked operation or bypass doctrine. The replacement will be checked again.", arguments.CanonicalJSON(), strings.Join(violations, "\n"))
}

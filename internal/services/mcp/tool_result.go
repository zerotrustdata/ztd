// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package mcp

import (
	"encoding/json"
	"fmt"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// marshalResult encodes a typed tool result as a single JSON text content item.
func marshalResult[T any](toolName string, result T) (CallToolResult, error) {
	body, err := json.Marshal(result)
	if err != nil {
		return CallToolResult{}, fmt.Errorf("%s: %w: %w", toolName, constants.ErrMCPMarshalResult, err)
	}
	return CallToolResult{
		Content: []TextContent{{Type: "text", Text: string(body)}},
	}, nil
}

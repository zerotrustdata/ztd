// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package auth

import (
	"context"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// RequestorUserID returns the transport-bound user for governed attribution.
// Apps act for their delegated user; other principals use their authenticated
// user. This value must not authorize human-only operations.
func RequestorUserID(ctx context.Context) string {
	key := constants.ContextKeyUserID
	if appID, _ := ctx.Value(constants.ContextKeyAppID).(string); appID != "" {
		key = constants.ContextKeyDelegatedUserID
	}
	userID, _ := ctx.Value(key).(string)
	return userID
}

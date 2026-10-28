// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package auth

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestRequestorUserID(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, app, user, delegate, want string
	}{
		{name: "anonymous"},
		{name: "authenticated user", user: "owner", want: "owner"},
		{name: "app delegation", app: "app", delegate: "requestor", want: "requestor"},
		{name: "app uses delegation only", app: "app", user: "owner", delegate: "requestor", want: "requestor"},
		{name: "app cannot borrow authenticated user", app: "app", user: "owner"},
		{name: "delegation requires an app", delegate: "requestor"},
		{name: "human cannot borrow delegation", user: "owner", delegate: "requestor", want: "owner"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.WithValue(t.Context(), constants.ContextKeyAppID, tc.app)
			ctx = context.WithValue(ctx, constants.ContextKeyUserID, tc.user)
			ctx = context.WithValue(ctx, constants.ContextKeyDelegatedUserID, tc.delegate)
			assert.Equal(t, tc.want, RequestorUserID(ctx))
		})
	}
}

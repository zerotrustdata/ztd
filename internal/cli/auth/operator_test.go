// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package auth

import (
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetLocalOSUser(t *testing.T) {
	user := getLocalOSUser()
	require.NotNil(t, user)
	assert.NotEmpty(t, user.Username)
	assert.NotEmpty(t, user.UID)
	assert.NotEmpty(t, user.GID)

	// On Windows, SID should be populated
	if runtime.GOOS == "windows" {
		assert.NotEmpty(t, user.SID)
	}
}

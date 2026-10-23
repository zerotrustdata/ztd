// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package authcmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAuthCmd(t *testing.T) {
	t.Parallel()
	t.Run("auth command has correct use and description", func(t *testing.T) {
		t.Parallel()
		cmd := Cmd()
		assert.Equal(t, "auth", cmd.Use)
		assert.Contains(t, cmd.Short, "Authentication")
		assert.Contains(t, cmd.Long, "mTLS")
	})
}

func TestEnrollUserCmd(t *testing.T) {
	t.Parallel()
	t.Run("user command has correct use", func(t *testing.T) {
		t.Parallel()
		cmd := enrollUserCmd()
		assert.Equal(t, "user", cmd.Use)
		assert.Contains(t, cmd.Short, "Enroll")
	})
}

// TestEnrollCmd_Parent verifies the enroll parent command has no RunE
// (cobra prints help and exits non-zero when invoked without a subcommand)
// and registers the user subcommand. The operator subcommand was removed
// when the unauthenticated operator enrollment bypass was closed (Phase 4);
// operators now enroll through the owner-approved platform enrollment
// protocol. The gui subcommand was removed in Phase 5 of the v2.1.8
// browser frontend connection UX rollout — `gw connect` is the sole local
// browser connection workflow.
func TestEnrollCmd_Parent(t *testing.T) {
	t.Parallel()
	cmd := enrollCmd()
	assert.Equal(t, "enroll", cmd.Use)
	assert.Nil(t, cmd.RunE, "parent enroll command must have no RunE so cobra prints help on bare invocation")

	names := map[string]bool{}
	for _, sub := range cmd.Commands() {
		names[sub.Name()] = true
	}
	assert.True(t, names["user"], "enroll parent must register the user subcommand")
	assert.True(t, names["pending"], "enroll parent must register the pending subcommand")
	assert.True(t, names["list"], "enroll parent must register the list subcommand")
	assert.True(t, names["approve"], "enroll parent must register the approve subcommand")
	assert.True(t, names["deny"], "enroll parent must register the deny subcommand")
	assert.True(t, names["revoke"], "enroll parent must register the revoke subcommand")
	assert.False(t, names["operator"], "enroll parent must NOT register the removed operator subcommand")
	assert.False(t, names["gui"], "enroll parent must NOT register the removed gui subcommand")
}

func TestAuthCommandFlags(t *testing.T) {
	t.Parallel()
	t.Run("enroll has no count flag", func(t *testing.T) {
		t.Parallel()
		cmd := enrollUserCmd()
		countFlag := cmd.Flags().Lookup("count")
		assert.Nil(t, countFlag)
	})

	t.Run("enroll has no ttl flag", func(t *testing.T) {
		t.Parallel()
		cmd := enrollUserCmd()
		ttlFlag := cmd.Flags().Lookup("ttl")
		assert.Nil(t, ttlFlag)
	})
}

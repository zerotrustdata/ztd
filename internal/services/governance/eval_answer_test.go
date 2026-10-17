// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package governance

import (
	"testing"

	"github.com/zerotrustdata/ztd/internal/constants"

	"github.com/stretchr/testify/assert"
)

// TestEvalAnswerIsNotMutation verifies that EVAL_ANSWER is not treated as a mutation
// and does not require L3 verification.
func TestEvalAnswerIsNotMutation(t *testing.T) {
	t.Parallel()
	notary := constants.PostureNotary

	assert.False(t, L3Required(constants.ActionTypeEvalAnswer, notary), "EVAL_ANSWER should not be treated as a mutation")

	// Verify that actual mutations are still detected
	assert.True(t, L3Required(constants.ActionTypeExecuteBash, notary), "EXECUTE_BASH should be treated as a mutation")
	assert.True(t, L3Required(constants.ActionTypeFileEdit, notary), "FILE_EDIT should be treated as a mutation")
}

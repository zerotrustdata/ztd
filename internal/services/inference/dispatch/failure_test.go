// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package dispatch

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// Every non-UNSPECIFIED wire code names exactly one sentinel, and that
// sentinel maps back to the same code.
func TestFailureCodes_RoundTripEveryCode(t *testing.T) {
	for value, name := range operatorv1.InferenceDispatchFailureCode_name {
		code := operatorv1.InferenceDispatchFailureCode(value)
		if code == operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_UNSPECIFIED {
			continue
		}
		sentinel := FailureSentinel(code)
		require.NotNil(t, sentinel, "code %s has no sentinel", name)
		assert.Equal(t, code, FailureCode(sentinel), "code %s", name)
	}
}

func TestFailureCode_WrappedSentinelAndUnknown(t *testing.T) {
	assert.Equal(t, operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_TOOLS_UNSUPPORTED,
		FailureCode(fmt.Errorf("dispatch: %w", constants.ErrInferenceToolsUnsupported)))
	assert.Equal(t, operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_UNSPECIFIED,
		FailureCode(constants.ErrMethodNotAllowed))
	assert.Nil(t, FailureSentinel(operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_UNSPECIFIED))
	assert.Nil(t, FailureSentinel(operatorv1.InferenceDispatchFailureCode(9999)))
}

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestShellCommandFailure(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		summary string
		want    string
	}{
		{name: "zero exit succeeds", summary: `{"exit_code":0,"stdout":"ok\n","stderr":"","timed_out":false}`},
		{name: "non-zero exit reports stderr", summary: `{"exit_code":1,"stdout":"","stderr":"cat: missing: No such file or directory\n","timed_out":false,"error":"exit status 1"}`,
			want: "cat: missing: No such file or directory"},
		{name: "non-zero exit without stderr reports the error", summary: `{"exit_code":2,"stdout":"","stderr":"","timed_out":false,"error":"exit status 2"}`,
			want: "exit status 2"},
		{name: "non-zero exit without detail reports the status", summary: `{"exit_code":3,"stdout":"","stderr":"","timed_out":false}`,
			want: "command exited with status 3"},
		{name: "timeout fails", summary: `{"exit_code":-1,"stdout":"","stderr":"","timed_out":true,"error":"command timed out"}`,
			want: "command timed out"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			failure, err := shellCommandFailure(tc.summary)
			require.NoError(t, err)
			if tc.want == "" {
				assert.Nil(t, failure)
				return
			}
			require.NotNil(t, failure)
			assert.Equal(t, agentToolErrorExecution, failure.ErrorType)
			assert.Equal(t, tc.want, failure.Error)
		})
	}
}

func TestShellCommandFailure_RejectsUntypedResult(t *testing.T) {
	t.Parallel()
	_, err := shellCommandFailure("completed")
	require.ErrorIs(t, err, constants.ErrEvaluationDispatchFailed)
}

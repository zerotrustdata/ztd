// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package llamacpp

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

func TestComposeModelDigest(t *testing.T) {
	first := models.SHA256Hex([]byte("a"))
	second := models.SHA256Hex([]byte("b"))

	single, err := ComposeModelDigest([]string{first})
	require.NoError(t, err)
	assert.Equal(t, first, single)

	split, err := ComposeModelDigest([]string{first, second})
	require.NoError(t, err)
	assert.Equal(t, models.SHA256Hex([]byte(first+"\n"+second)), split)

	_, err = ComposeModelDigest(nil)
	require.ErrorIs(t, err, constants.ErrMissingRequiredField)
	_, err = ComposeModelDigest([]string{strings.ToUpper(first)})
	require.ErrorIs(t, err, constants.ErrInferenceEvidenceHashInvalid)
}

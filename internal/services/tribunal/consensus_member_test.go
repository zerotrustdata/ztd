// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package tribunal

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestConsensusMemberAppIDsMatchConstants(t *testing.T) {
	t.Parallel()
	require.Equal(t, constants.TribunalConsensusMemberAppIDs(), ConsensusMemberAppIDs())
}

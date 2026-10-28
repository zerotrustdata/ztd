// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"fmt"
	"net/http"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/zerotrustdata/ztd/internal/cli/api"
	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestIsGatewayEvidenceNotFound(t *testing.T) {
	t.Parallel()
	assert.True(t, isGatewayEvidenceNotFound(constants.ErrNotFound))
	assert.True(t, isGatewayEvidenceNotFound(os.ErrNotExist))
	assert.True(t, isGatewayEvidenceNotFound(fmt.Errorf("provider observation: gateway read: %w", api.DecodeGatewayError(http.StatusNotFound, nil))))
	assert.False(t, isGatewayEvidenceNotFound(api.DecodeGatewayError(http.StatusServiceUnavailable, []byte(`{"error":"status 404"}`))))
	assert.False(t, isGatewayEvidenceNotFound(fmt.Errorf("%w: status 404", constants.ErrHTTPStatusError)))
}

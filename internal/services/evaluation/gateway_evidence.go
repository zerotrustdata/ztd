// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"errors"
	"os"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func isGatewayEvidenceNotFound(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, constants.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		return true
	}
	return false
}

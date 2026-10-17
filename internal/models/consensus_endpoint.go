// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package models

import (
	"net/url"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// ValidateConsensusEndpoint requires an absolute HTTPS application endpoint.
// Credentials and fragments are not part of the deliberation contract.
func ValidateConsensusEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return constants.ErrConsensusEndpointInvalid
	}
	return nil
}

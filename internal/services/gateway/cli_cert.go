// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"crypto/x509"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/protocol"
)

// CLIIdentityFromCert returns the user and CLI session a CLI certificate's
// SPIFFE URI SAN names, or empty strings when no URI SAN is a CLI identity.
func CLIIdentityFromCert(cert *x509.Certificate) (userID, cliSessionID string) {
	if cert == nil {
		return "", ""
	}
	wid := protocol.NewWorkloadIdentity()
	for _, uri := range cert.URIs {
		uriStr := uri.String()
		uid, userOK := wid.ExtractUserID(uriStr)
		sid, sessionOK := wid.ExtractCLISessionID(uriStr)
		if userOK && sessionOK && wid.MatchesCLI(uriStr, uid, sid) {
			return uid, sid
		}
	}
	return "", ""
}

// ExtractUserIDFromCert extracts the user ID from a certificate's SPIFFE URI SAN.
func ExtractUserIDFromCert(cert *x509.Certificate) (string, error) {
	if cert == nil {
		return "", constants.ErrCLIL3CertNil
	}

	wid := protocol.NewWorkloadIdentity()
	for _, uri := range cert.URIs {
		if userID, ok := wid.ExtractUserID(uri.String()); ok {
			return userID, nil
		}
	}

	return "", constants.ErrCLIL3NoUserIDInCert
}

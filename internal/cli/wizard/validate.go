// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package wizard

import (
	"fmt"
	"net/url"

	"github.com/zerotrustdata/ztd/internal/cli/browserorigin"
)

// validatePublicBaseURL validates a public base URL.
// Must be an absolute https URL; http is allowed only for exact loopback hosts.
// Rejects user info, query, fragment, and unsupported schemes.
func validatePublicBaseURL(s string) error {
	if s == "" {
		return fmt.Errorf("public base URL is required")
	}
	u, err := url.Parse(s)
	if err != nil {
		return fmt.Errorf("wizard: validate public base URL: %w", err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return fmt.Errorf("URL must use http or https scheme, got %q", u.Scheme)
	}
	if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
		return fmt.Errorf("http scheme is only allowed for loopback hosts (localhost, 127.0.0.1, ::1)")
	}
	if u.User != nil {
		return fmt.Errorf("URL must not contain user info")
	}
	if u.RawQuery != "" {
		return fmt.Errorf("URL must not contain a query string")
	}
	if u.Fragment != "" {
		return fmt.Errorf("URL must not contain a fragment")
	}
	if u.Host == "" {
		return fmt.Errorf("URL must have a host")
	}
	return nil
}

// validatePasskeyRP validates the passkey RP ID and origin together.
// The RP ID must equal or be a registrable suffix of the origin hostname.
// Validation is delegated to the centralized browserorigin package so the
// wizard, explicit gateway flags, and `gw connect` share one implementation
// of origin parsing, RP ID derivation, and public-suffix rejection.
func validatePasskeyRP(rpID, origin string) error {
	parsed, err := browserorigin.Parse(origin)
	if err != nil {
		return err
	}
	_, err = browserorigin.ValidateRPID(parsed, rpID)
	return err
}

// validateDownstreamURL validates an optional downstream server URL.
// Must be an absolute http/https URL with a host. Rejects credentials and fragments.
func validateDownstreamURL(s string) error {
	if s == "" {
		return fmt.Errorf("downstream URL is required when routing is enabled")
	}
	u, err := url.Parse(s)
	if err != nil {
		return fmt.Errorf("wizard: validate downstream URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("downstream URL must use http or https scheme, got %q", u.Scheme)
	}
	if u.User != nil {
		return fmt.Errorf("downstream URL must not contain credentials")
	}
	if u.Fragment != "" {
		return fmt.Errorf("downstream URL must not contain a fragment")
	}
	if u.Host == "" {
		return fmt.Errorf("downstream URL must have a host")
	}
	return nil
}

// validateCORSOrigin validates a CORS origin.
// Must be an exact origin: scheme://host[:port] with no path, query, fragment, or user info.
// Validation is delegated to the centralized browserorigin package so the
// wizard, explicit gateway flags, and `gw connect` share one implementation
// of origin parsing and canonicalization.
func validateCORSOrigin(s string) error {
	_, err := browserorigin.Parse(s)
	return err
}

// isLoopbackHost returns true for localhost, 127.0.0.1, and ::1.
func isLoopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

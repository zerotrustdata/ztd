// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package mcp

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"syscall"

	"github.com/zerotrustdata/ztd/internal/constants"
)

var (
	semicolonPattern                 = regexp.MustCompile(`;\s*$`)
	validRefPattern                  = regexp.MustCompile(`^[a-zA-Z0-9_\-./~]+$`)
	k8sNamePattern                   = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	dangerousShellChars              = []string{";", "&", "|", "$", "`", "(", ")", "<", ">", "\n", "\r"}
	dangerousShellCharsWithBackslash = []string{"$", "`", "\\", ";", "&", "|", "(", ")", "<", ">", "\n", "\r"}
)

// privateIPAllowlist holds CIDR ranges that are permitted for internal HTTP
// probing/actuation despite being private addresses. This supports disconnected
// edge scenarios where internal endpoints must be reachable.
var (
	privateAllowlistMu sync.RWMutex
	privateAllowlist   []*net.IPNet
)

// isIPAllowed checks whether the given IP falls within the private IP allowlist.
func isIPAllowed(ip net.IP) bool {
	privateAllowlistMu.RLock()
	defer privateAllowlistMu.RUnlock()
	for _, ipNet := range privateAllowlist {
		if ipNet.Contains(ip) {
			return true
		}
	}
	return false
}

// isRestrictedIP reports whether ip is an address a network probe must not reach
// unless it is explicitly allowlisted. IsUnspecified matters on Linux: dialing
// 0.0.0.0 connects to the local host. IsLinkLocalMulticast is covered by
// IsMulticast; both are named so a future split of those checks stays obvious.
func isRestrictedIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalMulticast()
}

// validateProbeHost rejects literal restricted addresses and the localhost name.
// A hostname is accepted here and checked again after resolution by rejectRestrictedDial.
func validateProbeHost(host string) error {
	host = strings.ToLower(strings.TrimSpace(host))
	if strings.Contains(host, "localhost") || host == "127.0.0.1" || host == "::1" {
		ip := net.ParseIP(host)
		if ip == nil || !isIPAllowed(ip) {
			return fmt.Errorf("mcp: validate probe host: %w", constants.ErrMCPValidateURLLoopbackAddress)
		}
	}
	if ip := net.ParseIP(host); ip != nil && isRestrictedIP(ip) && !isIPAllowed(ip) {
		return fmt.Errorf("mcp: validate probe host: %w", constants.ErrMCPValidateURLPrivateAddress)
	}
	return nil
}

// rejectRestrictedDial is a net.Dialer Control hook. It runs after name
// resolution, so a hostname that resolves to a restricted address is rejected
// along with a literal one. An address that is not a parseable IP fails closed.
func rejectRestrictedDial(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("mcp: dial resolved address: %w", err)
	}
	if zone := strings.IndexByte(host, '%'); zone >= 0 {
		host = host[:zone]
	}
	ip := net.ParseIP(host)
	if ip == nil || (isRestrictedIP(ip) && !isIPAllowed(ip)) {
		return fmt.Errorf("mcp: dial resolved address: %w", constants.ErrMCPValidateURLPrivateAddress)
	}
	return nil
}

func validateSQLQuery(query string) error {
	query = strings.TrimSpace(query)

	if query == "" {
		return fmt.Errorf("mcp: validate SQL query: %w", constants.ErrMCPValidateSQLQueryEmpty)
	}

	if semicolonPattern.MatchString(query) {
		return fmt.Errorf("mcp: validate SQL query: %w", constants.ErrMCPValidateSQLQueryTrailingSemicolon)
	}

	return nil
}

func validateHTTPRequestURL(rawURL string) (*url.URL, error) {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("mcp: validate HTTP request URL: invalid URL format: %w", err)
	}

	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return nil, fmt.Errorf("mcp: validate HTTP request URL: %w", constants.ErrMCPValidateURLInvalidScheme)
	}

	if parsedURL.Host == "" {
		return nil, fmt.Errorf("mcp: validate HTTP request URL: %w", constants.ErrMCPValidateURLMissingHost)
	}

	if err := validateProbeHost(parsedURL.Hostname()); err != nil {
		return nil, fmt.Errorf("mcp: validate HTTP request URL: %w", err)
	}

	return parsedURL, nil
}

func validateProcNetPath(protocol string) error {
	allowedProtocols := map[string]bool{
		"tcp":  true,
		"udp":  true,
		"tcp6": true,
		"udp6": true,
		"raw":  true,
	}

	if !allowedProtocols[protocol] {
		return fmt.Errorf("mcp: validate proc net path: %w: %s", constants.ErrMCPValidateProcNetInvalidProtocol, protocol)
	}

	return nil
}

func validateGitRepoPath(path string) error {
	if err := validatePath(path, "git repo path"); err != nil {
		return err
	}
	return nil
}

func validateGitRef(ref string) error {
	if ref == "" {
		return fmt.Errorf("mcp: validate git ref: %w", constants.ErrMCPValidateRefEmpty)
	}

	cleanRef := strings.TrimSpace(ref)
	if cleanRef != ref {
		return fmt.Errorf("mcp: validate git ref: %w", constants.ErrMCPValidateRefWhitespace)
	}

	if strings.ContainsAny(ref, "\x00") {
		return fmt.Errorf("mcp: validate git ref: %w", constants.ErrMCPValidateRefNullBytes)
	}

	for _, char := range dangerousShellChars {
		if strings.Contains(ref, char) {
			return fmt.Errorf("mcp: validate git ref: %w: %q", constants.ErrMCPValidateRefDangerousChar, char)
		}
	}

	if strings.HasPrefix(ref, "/") || strings.HasPrefix(ref, "\\") {
		return fmt.Errorf("mcp: validate git ref: %w", constants.ErrMCPValidateRefAbsolutePath)
	}

	if !validRefPattern.MatchString(ref) {
		return fmt.Errorf("mcp: validate git ref: %w", constants.ErrMCPValidateRefInvalidChars)
	}

	return nil
}

func validateK8sResourceName(name string) error {
	if name == "" {
		return fmt.Errorf("mcp: validate K8s resource name: %w", constants.ErrMCPValidateK8sNameEmpty)
	}

	cleanName := strings.TrimSpace(name)
	if cleanName != name {
		return fmt.Errorf("mcp: validate K8s resource name: %w", constants.ErrMCPValidateK8sNameWhitespace)
	}

	if len(name) > 253 {
		return fmt.Errorf("mcp: validate K8s resource name: %w", constants.ErrMCPValidateK8sNameTooLong)
	}

	if !k8sNamePattern.MatchString(name) {
		return fmt.Errorf("mcp: validate K8s resource name: %w", constants.ErrMCPValidateK8sNameInvalidPattern)
	}

	if strings.ContainsAny(name, "\x00") {
		return fmt.Errorf("mcp: validate K8s resource name: %w", constants.ErrMCPValidateK8sNameNullBytes)
	}

	return nil
}

func validateK8sNamespace(namespace string) error {
	if namespace == "" {
		return fmt.Errorf("mcp: validate K8s namespace: %w", constants.ErrMCPValidateK8sNamespaceEmpty)
	}

	cleanNamespace := strings.TrimSpace(namespace)
	if cleanNamespace != namespace {
		return fmt.Errorf("mcp: validate K8s namespace: %w", constants.ErrMCPValidateK8sNamespaceWhitespace)
	}

	if len(namespace) > 63 {
		return fmt.Errorf("mcp: validate K8s namespace: %w", constants.ErrMCPValidateK8sNamespaceTooLong)
	}

	if !k8sNamePattern.MatchString(namespace) {
		return fmt.Errorf("mcp: validate K8s namespace: %w", constants.ErrMCPValidateK8sNamespaceInvalidPattern)
	}

	if strings.ContainsAny(namespace, "\x00") {
		return fmt.Errorf("mcp: validate K8s namespace: %w", constants.ErrMCPValidateK8sNamespaceNullBytes)
	}

	return nil
}

func validateCloudMetadataOperation(operation string) error {
	allowedOperations := map[string]bool{
		"detect":            true,
		"instance":          true,
		"region":            true,
		"availability_zone": true,
		"instance_type":     true,
		"all":               true,
	}

	if !allowedOperations[operation] {
		return fmt.Errorf("mcp: validate cloud metadata operation: %w: %s", constants.ErrMCPValidateCloudMetadataInvalidOperation, operation)
	}

	return nil
}

func validateFilePath(path string) error {
	if err := validatePath(path, "file path"); err != nil {
		return err
	}
	return nil
}

func validateSSHConfigPath(path string) error {
	if path == "" {
		return nil
	}
	return validateFilePath(path)
}

func validateKnownHostsPath(path string) error {
	if path == "" {
		return nil
	}
	return validateFilePath(path)
}

func validateHostname(hostname string) error {
	if hostname == "" {
		return fmt.Errorf("mcp: validate hostname: %w", constants.ErrMCPValidateHostnameEmpty)
	}

	cleanHostname := strings.TrimSpace(hostname)
	if cleanHostname != hostname {
		return fmt.Errorf("mcp: validate hostname: %w", constants.ErrMCPValidateHostnameWhitespace)
	}

	if strings.ContainsAny(hostname, "\x00") {
		return fmt.Errorf("mcp: validate hostname: %w", constants.ErrMCPValidateHostnameNullBytes)
	}

	for _, char := range dangerousShellChars {
		if strings.Contains(hostname, char) {
			return fmt.Errorf("mcp: validate hostname: %w: %q", constants.ErrMCPValidateHostnameDangerousChar, char)
		}
	}

	return nil
}

func validateHostnames(hostnames []string) error {
	if len(hostnames) == 0 {
		return fmt.Errorf("mcp: validate hostnames: %w", constants.ErrMCPValidateHostnamesEmpty)
	}

	for _, hostname := range hostnames {
		if err := validateHostname(hostname); err != nil {
			return fmt.Errorf("mcp: validate hostnames: %w", err)
		}
	}

	return nil
}

func validateOperatorBinaryPath(path string) error {
	if path == "" {
		return nil
	}
	return validateFilePath(path)
}

func validateOperatorArgs(args []string) error {
	if args == nil {
		return nil
	}

	for _, arg := range args {
		if strings.ContainsAny(arg, "\x00") {
			return fmt.Errorf("mcp: validate operator args: %w", constants.ErrMCPValidateOperatorArgsNullBytes)
		}

		for _, char := range dangerousShellCharsWithBackslash {
			if strings.Contains(arg, char) {
				return fmt.Errorf("mcp: validate operator args: %w: %q", constants.ErrMCPValidateOperatorArgsDangerousChar, char)
			}
		}
	}

	return nil
}

func validatePath(path, context string) error {
	if path == "" {
		return fmt.Errorf("mcp: validate %s: %w", context, constants.ErrMCPValidatePathEmpty)
	}

	cleanPath := strings.TrimSpace(path)
	if cleanPath != path {
		return fmt.Errorf("mcp: validate %s: %w", context, constants.ErrMCPValidatePathWhitespace)
	}

	if strings.Contains(path, "..") {
		return fmt.Errorf("mcp: validate %s: %w", context, constants.ErrMCPValidatePathParentDirRef)
	}

	if strings.ContainsAny(path, "\x00") {
		return fmt.Errorf("mcp: validate %s: %w", context, constants.ErrMCPValidatePathNullBytes)
	}

	return nil
}

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gateway

import (
	"encoding/json"
	"net"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/network"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestResolveGatewayCertificateIdentity(t *testing.T) {
	tests := []struct {
		name         string
		certMode     string
		identityFile string
		setupFile    func(t *testing.T, fileSvc fs.RuntimeFileService) string
		wantErr      bool
		errContains  string
		checkResult  func(t *testing.T, extraIPs []net.IP, extraDNSNames []string)
	}{
		{
			name:     "loads identity from file",
			certMode: "full",
			setupFile: func(t *testing.T, fileSvc fs.RuntimeFileService) string {
				t.Helper()
				identity := network.NetworkIdentity{
					IPs:       []string{"192.0.2.10", "2001:db8::10"},
					Hostnames: []string{"gateway.test.internal"},
				}
				identityData, err := json.Marshal(identity)
				require.NoError(t, err)
				require.NoError(t, fileSvc.WriteFile(t.Context(), constants.NetworkIdentityFilename, identityData, constants.PermFilePrivate))
				return fileSvc.Resolve(constants.NetworkIdentityFilename)
			},
			wantErr: false,
			checkResult: func(t *testing.T, extraIPs []net.IP, extraDNSNames []string) {
				t.Helper()
				assert.Equal(t, []net.IP{net.ParseIP("192.0.2.10")}, extraIPs)
				assert.Contains(t, extraDNSNames, "gateway.test.internal")
				assert.Contains(t, extraDNSNames, "localhost")
			},
		},
		{
			name:         "falls back to detection when file missing",
			certMode:     "full",
			identityFile: "",
			wantErr:      false,
			checkResult: func(t *testing.T, extraIPs []net.IP, extraDNSNames []string) {
				t.Helper()
				assert.NotEmpty(t, extraIPs, "should detect at least one IP")
				assert.NotEmpty(t, extraDNSNames, "should have DNS names")
				assert.Contains(t, extraDNSNames, "localhost")
			},
		},
		{
			name:         "localhost filters loopback only",
			certMode:     "localhost",
			identityFile: "",
			wantErr:      false,
			checkResult: func(t *testing.T, extraIPs []net.IP, extraDNSNames []string) {
				t.Helper()
				for _, ip := range extraIPs {
					assert.True(t, ip.IsLoopback(), "all IPs should be loopback")
				}
				assert.Equal(t, []string{"localhost"}, extraDNSNames)
			},
		},
		{
			name:     "malformed identity file",
			certMode: "full",
			setupFile: func(t *testing.T, fileSvc fs.RuntimeFileService) string {
				t.Helper()
				require.NoError(t, fileSvc.WriteFile(t.Context(), constants.NetworkIdentityFilename, []byte("not-json"), constants.PermFilePrivate))
				return fileSvc.Resolve(constants.NetworkIdentityFilename)
			},
			wantErr:     true,
			errContains: "unmarshal",
		},
	}

	for _, tt := range tests {

		t.Run(tt.name, func(t *testing.T) {
			logger := testutil.NewTestLogger()
			dir := testutil.TempDir(t)
			fileSvc, err := fs.NewRuntimeFileService(dir, logger)
			require.NoError(t, err)
			identityFile := tt.identityFile

			if tt.setupFile != nil {
				identityFile = tt.setupFile(t, fileSvc)
			}

			detector := network.NewDetector(logger)
			extraIPs, extraDNSNames, err := resolveGatewayCertificateIdentity(tt.certMode, identityFile, fileSvc, detector, logger)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, strings.ToLower(err.Error()), tt.errContains)
				}
			} else {
				require.NoError(t, err)
				if tt.checkResult != nil {
					tt.checkResult(t, extraIPs, extraDNSNames)
				}
			}
		})
	}
}

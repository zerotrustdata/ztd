// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package shared

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
	"github.com/zerotrustdata/ztd/internal/services/keystore/keystoretest"
	"github.com/zerotrustdata/ztd/internal/services/vault"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestProvisionRecoveryKey_BeforeRuntimeTreeCreation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		accept bool
	}{
		{name: "explicit acceptance stores the key", accept: true},
		{name: "noninteractive refusal leaves the key absent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fileSvc, err := fs.NewRuntimeFileService(testutil.TempDir(t), testutil.NewTestLogger())
			require.NoError(t, err)
			kr := keystoretest.NewMemoryKeyring()
			var output bytes.Buffer

			err = provisionRecoveryKey(strings.NewReader(""), &output, fileSvc, kr, keystore.RecoveryRoleGateway, tc.accept)
			if tc.accept {
				require.NoError(t, err)
				key, err := kr.RetrieveMasterKey()
				require.NoError(t, err)
				require.Len(t, key, vault.KeySize)
				assert.Contains(t, output.String(), "--accept-unrecoverable")
				output.Reset()
				require.NoError(t, provisionRecoveryKey(strings.NewReader(""), &output, fileSvc, kr, keystore.RecoveryRoleGateway, false))
				assert.Empty(t, output.String(), "an existing key needs no second ceremony")
				retained, err := kr.RetrieveMasterKey()
				require.NoError(t, err)
				assert.Equal(t, key, retained)
			} else {
				require.ErrorIs(t, err, constants.ErrKeyStoreRecoveryRequired)
				_, err := kr.RetrieveMasterKey()
				require.ErrorIs(t, err, constants.ErrKeyStoreKeyNotFound)
			}
			exists, err := fileSvc.FileExists(t.Context(), constants.SecretsDirname)
			require.NoError(t, err)
			assert.False(t, exists, "the ceremony must not depend on a runtime tree")
		})
	}
}

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package keystoretest

import (
	"testing"

	"github.com/zerotrustdata/ztd/internal/services/keystore"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// ProvisionMasterKeyFile writes a fresh external master key file in an isolated
// temp directory outside any runtime root and returns its path, in the form
// keystore.Options.MasterKeyFile and --master-key-file select.
func ProvisionMasterKeyFile(t *testing.T) string {
	t.Helper()
	path, err := keystore.ProvisionMasterKeyFile(testutil.TempDir(t))
	if err != nil {
		t.Fatalf("provision master key file: %v", err)
	}
	return path
}

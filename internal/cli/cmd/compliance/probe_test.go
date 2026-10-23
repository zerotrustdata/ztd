// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package compliancecmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/services/compliance"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
	"github.com/zerotrustdata/ztd/internal/services/keystore/keystoretest"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestProbe_EvaluateFailClosed(t *testing.T) {
	fileSvc, _ := cmdtest.NewCmdTestEnv(t)
	ks, err := keystore.NewWithKeyringAndFS(testutil.NewTestLogger(), keystoretest.NewMemoryKeyring(), fileSvc)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())
	require.NoError(t, ks.InitVault())

	tmp := testutil.TempDir(t)
	cat := compliance.KSICatalog{Version: "1.0", Source: "test", KSIs: []compliance.KSI{{
		ID: "KSI-CMT-01", Title: "T", Category: compliance.KSICategoryCMT,
		ControlRefs: []string{"CM-3"}, ApplicableClasses: []compliance.CertificationClass{compliance.ClassC},
		ValidationCycle: compliance.ValidationCycleMachine,
	}}}
	data, _ := json.Marshal(cat)
	path := filepath.Join(tmp, "cat.json")
	os.WriteFile(path, data, 0644)
	loaded, err := loadKSICatalog(path)
	if err != nil {
		t.Fatalf("catalog load: %v", err)
	}
	rs, evalErr := evaluateKSIs(context.Background(), fileSvc, ks, loaded, compliance.ClassC, testEvaluationBinding(t))
	t.Logf("evaluation error: %v", evalErr)
	t.Logf("resultSet is nil: %v", rs == nil)
	if rs != nil {
		t.Logf("results: %d, satisfied: %d, notsat: %d", len(rs.Results), rs.SatisfiedCount(), rs.NotSatisfiedCount())
	}
}

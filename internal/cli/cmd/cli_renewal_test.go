// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package cmd

import (
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/cli/serve"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

func TestRootPreRun_NoCLIRenewalDoesNotConstructIdentityDependencies(t *testing.T) {
	oldLoad, oldFiles := shared.ConfigLoad, shared.NewFileSvc
	t.Cleanup(func() { shared.ConfigLoad, shared.NewFileSvc = oldLoad, oldFiles; config.SetEndpointOverride("") })
	shared.ConfigLoad = func(string) (*config.Config, error) { t.Fatal("renewal loaded configuration"); return nil, nil }
	shared.NewFileSvc = func(string, *slog.Logger) (fs.RuntimeFileService, error) {
		t.Fatal("renewal constructed runtime files")
		return nil, nil
	}
	root := NewRootCmd("dev", serve.VersionInfo{})
	ran := false
	probe := shared.DeclareNoCLIRenewal(&cobra.Command{Use: "probe", RunE: func(*cobra.Command, []string) error { ran = true; return nil }})
	root.AddCommand(probe)
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	// An explicit endpoint avoids the independent launch-profile read.
	root.SetArgs([]string{"--endpoint", "localhost", "probe"})
	require.NoError(t, root.Execute())
	require.True(t, ran)
}

func TestNeedsCLIRenewal_RenewsOnlyGatewayClientCommands(t *testing.T) {
	root := NewRootCmd("dev", serve.VersionInfo{})
	for _, test := range []struct {
		path  []string
		renew bool
	}{
		{[]string{"gw", "start"}, false}, {[]string{"gw", "stop"}, false},
		{[]string{"gw", "status"}, false}, {[]string{"gw", "data"}, true},
		{[]string{"operator", "start"}, false}, {[]string{"operator", "run"}, true},
		{[]string{"operator", "deploy"}, true}, {[]string{"vault"}, false},
		{[]string{"version"}, false}, {[]string{"swagger"}, false},
		{[]string{"report"}, false},
		{[]string{"compliance"}, false}, {[]string{"mcp", "agent", "list"}, false},
		{[]string{"public", "verify-proof"}, false}, {[]string{"public", "publish"}, true},
		{[]string{"eval", "formations"}, false}, {[]string{"eval", "suites"}, false},
		{[]string{"test", "integration"}, false}, {[]string{"autochthon"}, false},
		{[]string{"mcp"}, true}, {[]string{"tui"}, true},
		{[]string{"auth", "approve"}, true}, {[]string{"eval", "runs", "execute"}, true},
	} {
		t.Run(strings.Join(test.path, " "), func(t *testing.T) {
			cmd, _, err := root.Find(test.path)
			require.NoError(t, err)
			require.Equal(t, test.renew, shared.NeedsCLIRenewal(cmd))
		})
	}
}

func TestLocalOnlyLogoutSkipsRenewalForItsWholeRun(t *testing.T) {
	root := NewRootCmd("dev", serve.VersionInfo{})
	command, _, err := root.Find([]string{"logout"})
	require.NoError(t, err)
	require.True(t, shared.NeedsCLIRenewal(command))
	require.NoError(t, command.Flags().Set("local-only", "true"))
	require.False(t, shared.NeedsCLIRenewal(command))
	oldLoad := shared.ConfigLoad
	t.Cleanup(func() { shared.ConfigLoad = oldLoad })
	shared.ConfigLoad = func(string) (*config.Config, error) {
		t.Fatal("local-only logout constructed renewal dependencies")
		return nil, nil
	}
	renewCLIIdentity(command)
	startCLIRenewal(command)()
}

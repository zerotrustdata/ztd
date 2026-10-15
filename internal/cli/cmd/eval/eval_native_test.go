// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package eval

import (
	"log/slog"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	harnessclient "github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	harnessconfig "github.com/zerotrustdata/ztd/internal/tools/agent_harness/config"
)

func panickingNativeEvalDeps(t *testing.T) nativeEvalDeps {
	t.Helper()
	return nativeEvalDeps{
		configLoader: func(string) (*config.Config, error) {
			t.Fatal("config loader called")
			return nil, nil
		},
		fileSvcFactory: func(string, *slog.Logger) (fs.RuntimeFileService, error) {
			t.Fatal("file service factory called")
			return nil, nil
		},
		clientFactory: func(harnessconfig.Config) (*harnessclient.Client, error) {
			t.Fatal("client factory called")
			return nil, nil
		},
		authLoader: func(fs.RuntimeFileService, *config.Config) (*auth.ClientAuthContext, error) {
			t.Fatal("auth loader called")
			return nil, nil
		},
		now:   time.Now,
		newID: func() (string, error) { return "run-id", nil },
	}
}

func TestEvalCmd_ContainsProgramGroups(t *testing.T) {
	command := evalCmdWithConfig(panickingNativeEvalDeps(t))
	names := make([]string, 0, len(command.Commands()))
	for _, child := range command.Commands() {
		names = append(names, child.Name())
	}
	assert.ElementsMatch(t, []string{
		"backup", "campaigns", "conformance", "formations", "gates", "models", "restore", "rollout", "runs", "suites",
	}, names)
	assert.Len(t, names, 10)
	assert.Empty(t, command.Aliases, "eval has no aliases")
	for _, child := range command.Commands() {
		assert.Empty(t, child.Aliases, "group %s must have no aliases", child.Name())
	}

	var conformance *cobra.Command
	for _, child := range command.Commands() {
		if child.Name() == "conformance" {
			conformance = child
			break
		}
	}
	require.NotNil(t, conformance)
	conformanceNames := make([]string, 0, len(conformance.Commands()))
	for _, child := range conformance.Commands() {
		conformanceNames = append(conformanceNames, child.Name())
	}
	assert.ElementsMatch(t, []string{"run", "verify", "report"}, conformanceNames)

	var gates *cobra.Command
	for _, child := range command.Commands() {
		if child.Name() == "gates" {
			gates = child
			break
		}
	}
	require.NotNil(t, gates)
	gatesNames := make([]string, 0, len(gates.Commands()))
	for _, child := range gates.Commands() {
		gatesNames = append(gatesNames, child.Name())
	}
	assert.ElementsMatch(t, []string{"inference", "probe"}, gatesNames)

}

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package testcmd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

func TestTestCoverageCmd_PkgFlagChangesTargetPackage(t *testing.T) {
	cmd := testCoverageCmd()
	require.NotNil(t, cmd)

	require.NoError(t, cmd.Flags().Set("pkg", "./internal/services/auth"))
	flag := cmd.Flags().Lookup("pkg")
	require.NotNil(t, flag)
	assert.Equal(t, "./internal/services/auth", flag.Value.String())
}

func TestTestCoverageCmd_VerboseFlagAddsVFlag(t *testing.T) {
	cmd := testCoverageCmd()
	require.NotNil(t, cmd)

	require.NoError(t, cmd.Flags().Set("verbose", "true"))
	flag := cmd.Flags().Lookup("verbose")
	require.NotNil(t, flag)
	assert.Equal(t, "true", flag.Value.String())
}

func TestTestCoverageCmd_DelegatesThresholdEnforcementToMakefile(t *testing.T) {
	var capturedName string
	var capturedArgs []string
	runner := func(name string, args ...string) error {
		capturedName = name
		capturedArgs = args
		return nil
	}
	cmd := testCoverageCmdWithRunner(runner)
	require.NoError(t, cmd.Flags().Set("pkg", "./internal/services/auth"))
	require.NoError(t, cmd.Flags().Set("verbose", "true"))

	require.NoError(t, cmd.RunE(cmd, nil))

	assert.Equal(t, "make", capturedName)
	assert.Equal(t, []string{"test-coverage", "PKG=./internal/services/auth", "VERBOSE=1"}, capturedArgs)
}

func TestTestCoverageCmd_MakefileFailureWrapsCoverageError(t *testing.T) {
	runnerErr := fmt.Errorf("coverage threshold failed")
	cmd := testCoverageCmdWithRunner(func(string, ...string) error { return runnerErr })

	err := cmd.RunE(cmd, nil)

	require.Error(t, err)
	require.ErrorIs(t, err, constants.ErrCoverageTestsFailed)
	assert.ErrorIs(t, err, runnerErr)
}

// recordingE2ERunner captures the arguments passed to the runner and returns
// a fixed (code, err) pair. Used to verify argument propagation without
// starting platform tests.
func recordingE2ERunner(code int, err error, captured *[]string) e2eCommandRunner {
	return func(_ context.Context, _ string, args ...string) (int, error) {
		*captured = args
		return code, err
	}
}

func TestTestE2ECmd_PropagatesArgumentsAndRaceFlag(t *testing.T) {
	var captured []string
	cmd := testE2ECmdWithRunner(recordingE2ERunner(0, nil, &captured))
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	require.NoError(t, cmd.RunE(cmd, nil))

	// Core flags are always present.
	assert.Contains(t, captured, "test")
	assert.Contains(t, captured, "-tags=e2e")
	assert.Contains(t, captured, "-count=1")
	assert.Contains(t, captured, "-parallel=1")
	assert.Contains(t, captured, "-v")
	assert.Contains(t, captured, "./test/e2e/...")
	// -race is present on non-Windows platforms.
	if runtime.GOOS != "windows" {
		assert.Contains(t, captured, "-race")
	}
	idx := -1
	for i, a := range captured {
		if a == "-run" {
			idx = i
			break
		}
	}
	require.GreaterOrEqual(t, idx, 0, "captured args should contain the approved-stack default -run: %v", captured)
	require.Greater(t, len(captured), idx+1, "captured args should have a value after -run: %v", captured)
	assert.Equal(t, "^(TestApprovedRestart_|TestAuth_|TestCommandRoundtrip_|TestCompliance_|TestConsole_|TestGateway_|TestGovernance_|TestOperatorRegistry_|TestPlatform_FullBootstrap$|TestPubSub_|TestSSE_)", captured[idx+1])
}

func TestTestE2ECmd_RunFlagAppendsRegexp(t *testing.T) {
	var captured []string
	cmd := testE2ECmdWithRunner(recordingE2ERunner(0, nil, &captured))
	require.NoError(t, cmd.Flags().Set("run", "TestApproved"))

	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	require.NoError(t, cmd.RunE(cmd, nil))

	idx := -1
	for i, a := range captured {
		if a == "-run" {
			idx = i
			break
		}
	}
	require.GreaterOrEqual(t, idx, 0, "captured args should contain -run: %v", captured)
	require.Greater(t, len(captured), idx+1, "captured args should have value after -run: %v", captured)
	assert.Equal(t, "TestApproved", captured[idx+1])
}

func TestTestE2ECmd_TimeoutFlagDefaultsAndOverrides(t *testing.T) {
	timeoutArg := func(captured []string) string {
		for i, a := range captured {
			if a == "-timeout" && i+1 < len(captured) {
				return captured[i+1]
			}
		}
		return ""
	}

	var defaulted []string
	cmd := testE2ECmdWithRunner(recordingE2ERunner(0, nil, &defaulted))
	require.NoError(t, cmd.RunE(cmd, nil))
	assert.Equal(t, "5m0s", timeoutArg(defaulted), "default stays 300 seconds")

	var raised []string
	cmd = testE2ECmdWithRunner(recordingE2ERunner(0, nil, &raised))
	require.NoError(t, cmd.Flags().Set("timeout", "45m"))
	require.NoError(t, cmd.RunE(cmd, nil))
	assert.Equal(t, "45m0s", timeoutArg(raised))
}

func TestTestE2ECmd_NonzeroExitWrapsErrE2ETestsFailed(t *testing.T) {
	runnerErr := fmt.Errorf("child process failed")
	cmd := testE2ECmdWithRunner(recordingE2ERunner(2, runnerErr, &[]string{}))
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(cmd, nil)
	require.Error(t, err)
	require.ErrorIs(t, err, constants.ErrE2ETestsFailed)
	assert.ErrorIs(t, err, runnerErr)
}

func TestTestE2ECmd_NonzeroExitWithoutErrorWrapsErrE2ETestsFailed(t *testing.T) {
	cmd := testE2ECmdWithRunner(recordingE2ERunner(1, nil, &[]string{}))
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(cmd, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrE2ETestsFailed)
}

func TestTestSummaryCmd_NoTestVaultReturnsMessage(t *testing.T) {
	tmpDir := cmdtest.ChdirTemp(t)

	protocolDir := filepath.Join(tmpDir, "protocol", "constants")
	require.NoError(t, os.MkdirAll(protocolDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(protocolDir, "paths.json"), []byte(cmdtest.MinimalPathsJSON(t)), 0o644))

	cmd := testSummaryCmd()
	cmd.SetContext(context.Background())
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(cmd, nil)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Test vault directory not found")
}

func TestTestSummaryCmd_EmptyTestVaultReturnsMessage(t *testing.T) {
	tmpDir := cmdtest.ChdirTemp(t)

	protocolDir := filepath.Join(tmpDir, "protocol", "constants")
	require.NoError(t, os.MkdirAll(protocolDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(protocolDir, "paths.json"), []byte(cmdtest.MinimalPathsJSON(t)), 0o644))

	fileSvc, err := fs.NewRuntimeFileService("", nil)
	require.NoError(t, err)
	require.NoError(t, fileSvc.MkdirAll(context.Background(), constants.TestVaultDirname, constants.PermDirPrivate))

	cmd := testSummaryCmd()
	cmd.SetContext(context.Background())
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err = cmd.RunE(cmd, nil)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "No chaos test runs found")
}

func TestTestUnitCmd_StructureAndFlags(t *testing.T) {
	cmd := testUnitCmd()
	assert.Equal(t, "unit", cmd.Use)
	assert.NotNil(t, cmd.RunE)
	runFlag := cmd.Flags().Lookup("run")
	require.NotNil(t, runFlag)
	assert.Empty(t, runFlag.DefValue)
	pkgFlag := cmd.Flags().Lookup("pkg")
	require.NotNil(t, pkgFlag)
	assert.Equal(t, "./...", pkgFlag.DefValue)
}

func TestTestUnitCmd_DelegatesToMakefileTarget(t *testing.T) {
	var captured []string
	cmd := testUnitCmdWithRunner(recordingE2ERunner(0, nil, &captured))

	require.NoError(t, cmd.RunE(cmd, nil))

	assert.Equal(t, []string{"test-unit"}, captured)
}

func TestTestUnitCmd_RunnerFailureWrapsUnitError(t *testing.T) {
	runnerErr := fmt.Errorf("child process failed")
	cmd := testUnitCmdWithRunner(recordingE2ERunner(2, runnerErr, &[]string{}))

	err := cmd.RunE(cmd, nil)

	require.Error(t, err)
	require.ErrorIs(t, err, constants.ErrUnitTestsFailed)
	assert.ErrorIs(t, err, runnerErr)
}

func TestTestUnitCmd_NonzeroExitWrapsUnitError(t *testing.T) {
	cmd := testUnitCmdWithRunner(recordingE2ERunner(2, nil, &[]string{}))

	err := cmd.RunE(cmd, nil)

	require.Error(t, err)
	require.ErrorIs(t, err, constants.ErrUnitTestsFailed)
	assert.Contains(t, err.Error(), "exit code 2")
}

func TestTestUnitCmd_RunFlagAppendsRegexp(t *testing.T) {
	var captured []string
	cmd := testUnitCmdWithRunner(recordingE2ERunner(0, nil, &captured))
	require.NoError(t, cmd.Flags().Set("pkg", "./internal/services/evaluation"))
	require.NoError(t, cmd.Flags().Set("run", "TestEvaluationContract"))

	require.NoError(t, cmd.RunE(cmd, nil))

	assert.Contains(t, captured, "-timeout")
	assert.Contains(t, captured, "./internal/services/evaluation")
	assert.NotContains(t, captured, "./...")
	idx := -1
	for i, arg := range captured {
		if arg == "-run" {
			idx = i
			break
		}
	}
	require.GreaterOrEqual(t, idx, 0)
	require.Greater(t, len(captured), idx+1)
	assert.Equal(t, "TestEvaluationContract", captured[idx+1])
}

func TestTestUnitCmd_TargetedOnlyPkg(t *testing.T) {
	var captured []string
	cmd := testUnitCmdWithRunner(recordingE2ERunner(0, nil, &captured))
	require.NoError(t, cmd.Flags().Set("pkg", "./internal/services/evaluation"))

	require.NoError(t, cmd.RunE(cmd, nil))

	assert.Contains(t, captured, "-timeout")
	assert.Contains(t, captured, "./internal/services/evaluation")
	assert.NotContains(t, captured, "-run")
}

func TestTestUnitCmd_TargetedOnlyRun(t *testing.T) {
	var captured []string
	cmd := testUnitCmdWithRunner(recordingE2ERunner(0, nil, &captured))
	require.NoError(t, cmd.Flags().Set("run", "TestSpecificUnitTest"))

	require.NoError(t, cmd.RunE(cmd, nil))

	assert.Contains(t, captured, "-timeout")
	assert.Contains(t, captured, "./...")
	idx := -1
	for i, arg := range captured {
		if arg == "-run" {
			idx = i
			break
		}
	}
	require.GreaterOrEqual(t, idx, 0)
	require.Greater(t, len(captured), idx+1)
	assert.Equal(t, "TestSpecificUnitTest", captured[idx+1])
}

func TestTestUnitCmd_TargetedRunnerFailureWrapsUnitError(t *testing.T) {
	runnerErr := fmt.Errorf("child process failed")
	cmd := testUnitCmdWithRunner(recordingE2ERunner(2, runnerErr, &[]string{}))
	require.NoError(t, cmd.Flags().Set("pkg", "./internal/services/evaluation"))

	err := cmd.RunE(cmd, nil)

	require.Error(t, err)
	require.ErrorIs(t, err, constants.ErrUnitTestsFailed)
	assert.ErrorIs(t, err, runnerErr)
}

func TestTestUnitCmd_TargetedNonzeroExitWrapsUnitError(t *testing.T) {
	cmd := testUnitCmdWithRunner(recordingE2ERunner(2, nil, &[]string{}))
	require.NoError(t, cmd.Flags().Set("run", "TestEvaluationContract"))

	err := cmd.RunE(cmd, nil)

	require.Error(t, err)
	require.ErrorIs(t, err, constants.ErrUnitTestsFailed)
	assert.Contains(t, err.Error(), "exit code 2")
}

func TestTestIntegrationCmd_StructureAndFlags(t *testing.T) {
	cmd := testIntegrationCmd()
	assert.Equal(t, "integration", cmd.Use)
	assert.NotNil(t, cmd.RunE)
	runFlag := cmd.Flags().Lookup("run")
	require.NotNil(t, runFlag)
	assert.Empty(t, runFlag.DefValue)
	verboseFlag := cmd.Flags().ShorthandLookup("v")
	require.NotNil(t, verboseFlag)
	assert.Equal(t, "false", verboseFlag.DefValue)
	pkgFlag := cmd.Flags().Lookup("pkg")
	require.NotNil(t, pkgFlag)
	assert.Equal(t, "./...", pkgFlag.DefValue)
}

func TestTestIntegrationCmd_RunFlagAppendsRegexp(t *testing.T) {
	var captured []string
	cmd := testIntegrationCmdWithRunner(recordingE2ERunner(0, nil, &captured))
	require.NoError(t, cmd.Flags().Set("pkg", "./internal/services/gateway"))
	require.NoError(t, cmd.Flags().Set("run", "TestPublicMirror|TestPublicPublisher"))

	require.NoError(t, cmd.RunE(cmd, nil))

	assert.Contains(t, captured, "-tags=integration")
	assert.Contains(t, captured, "-count=1")
	assert.Contains(t, captured, "30m0s")
	assert.Contains(t, captured, "./internal/services/gateway")
	assert.NotContains(t, captured, "./...")
	if runtime.GOOS != "windows" {
		assert.Contains(t, captured, "-race")
	}
	idx := -1
	for i, arg := range captured {
		if arg == "-run" {
			idx = i
			break
		}
	}
	require.GreaterOrEqual(t, idx, 0)
	require.Greater(t, len(captured), idx+1)
	assert.Equal(t, "TestPublicMirror|TestPublicPublisher", captured[idx+1])
}

func TestTestIntegrationCmd_VerboseFlagAppendsGoTestVerbose(t *testing.T) {
	var captured []string
	cmd := testIntegrationCmdWithRunner(recordingE2ERunner(0, nil, &captured))
	require.NoError(t, cmd.Flags().Set("verbose", "true"))
	require.NoError(t, cmd.Flags().Set("pkg", "./internal/services/gateway"))

	require.NoError(t, cmd.RunE(cmd, nil))

	assert.Contains(t, captured, "-v")
	assert.Contains(t, captured, "./internal/services/gateway")
}

func TestTestIntegrationCmd_TargetedOnlyPkg(t *testing.T) {
	var captured []string
	cmd := testIntegrationCmdWithRunner(recordingE2ERunner(0, nil, &captured))
	require.NoError(t, cmd.Flags().Set("pkg", "./internal/services/gateway"))

	require.NoError(t, cmd.RunE(cmd, nil))

	assert.Contains(t, captured, "-tags=integration")
	assert.Contains(t, captured, "./internal/services/gateway")
	assert.NotContains(t, captured, "-run")
}

func TestTestIntegrationCmd_TargetedOnlyRun(t *testing.T) {
	var captured []string
	cmd := testIntegrationCmdWithRunner(recordingE2ERunner(0, nil, &captured))
	require.NoError(t, cmd.Flags().Set("run", "TestSpecificIntegrationTest"))

	require.NoError(t, cmd.RunE(cmd, nil))

	assert.Contains(t, captured, "-tags=integration")
	assert.Contains(t, captured, "./...")
	idx := -1
	for i, arg := range captured {
		if arg == "-run" {
			idx = i
			break
		}
	}
	require.GreaterOrEqual(t, idx, 0)
	require.Greater(t, len(captured), idx+1)
	assert.Equal(t, "TestSpecificIntegrationTest", captured[idx+1])
}

func TestTestIntegrationCmd_TargetedRunnerFailureWrapsIntegrationError(t *testing.T) {
	runnerErr := fmt.Errorf("child process failed")
	cmd := testIntegrationCmdWithRunner(recordingE2ERunner(2, runnerErr, &[]string{}))
	require.NoError(t, cmd.Flags().Set("pkg", "./internal/services/gateway"))

	err := cmd.RunE(cmd, nil)

	require.Error(t, err)
	require.ErrorIs(t, err, constants.ErrIntegrationTestsFailed)
	assert.ErrorIs(t, err, runnerErr)
}

func TestTestIntegrationCmd_TargetedNonzeroExitWrapsIntegrationError(t *testing.T) {
	cmd := testIntegrationCmdWithRunner(recordingE2ERunner(2, nil, &[]string{}))
	require.NoError(t, cmd.Flags().Set("run", "TestSpecificIntegrationTest"))

	err := cmd.RunE(cmd, nil)

	require.Error(t, err)
	require.ErrorIs(t, err, constants.ErrIntegrationTestsFailed)
	assert.Contains(t, err.Error(), "exit code 2")
}

func TestTestIntegrationCmd_DefaultOmitsRunFlag(t *testing.T) {
	var captured []string
	cmd := testIntegrationCmdWithRunner(recordingE2ERunner(0, nil, &captured))

	require.NoError(t, cmd.RunE(cmd, nil))

	assert.NotContains(t, captured, "-run")
}

func TestTestIntegrationCmd_RunnerFailureWrapsIntegrationError(t *testing.T) {
	runnerErr := fmt.Errorf("child process failed")
	cmd := testIntegrationCmdWithRunner(recordingE2ERunner(2, runnerErr, &[]string{}))

	err := cmd.RunE(cmd, nil)

	require.Error(t, err)
	require.ErrorIs(t, err, constants.ErrIntegrationTestsFailed)
	assert.ErrorIs(t, err, runnerErr)
}

func TestTestIntegrationCmd_NonzeroExitWrapsIntegrationError(t *testing.T) {
	cmd := testIntegrationCmdWithRunner(recordingE2ERunner(2, nil, &[]string{}))

	err := cmd.RunE(cmd, nil)

	require.Error(t, err)
	require.ErrorIs(t, err, constants.ErrIntegrationTestsFailed)
	assert.Contains(t, err.Error(), "exit code 2")
}

// fakeLintLocator returns each result in turn, repeating the last one.
func fakeLintLocator(results ...golangciLint) golangciLintLocator {
	return func(context.Context) golangciLint {
		next := results[0]
		if len(results) > 1 {
			results = results[1:]
		}
		return next
	}
}

func recordingLintRunner(calls *[][]string) e2eCommandRunner {
	return func(_ context.Context, name string, args ...string) (int, error) {
		*calls = append(*calls, append([]string{name}, args...))
		return 0, nil
	}
}

func runLintCmd(t *testing.T, cmd *cobra.Command, stdin string, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	require.NoError(t, cmd.ParseFlags(args))
	err := cmd.RunE(cmd, nil)
	return out.String(), err
}

const staleLintProblem = "was built with go1.26.2, older than go1.27.3 in go.mod"

func TestTestLintCmd_CurrentBinaryRunsWithoutPrompt(t *testing.T) {
	var calls [][]string
	cmd := testLintCmdWithDeps(recordingLintRunner(&calls), fakeLintLocator(golangciLint{
		path:     "/gobin/golangci-lint",
		shadowed: "/usr/local/bin/golangci-lint",
	}))

	out, err := runLintCmd(t, cmd, "")

	require.NoError(t, err)
	assert.Equal(t, [][]string{{"/gobin/golangci-lint", "run"}}, calls)
	assert.Contains(t, out, "Using /gobin/golangci-lint instead of /usr/local/bin/golangci-lint")
	assert.NotContains(t, out, "[Y/n]")
}

func TestTestLintCmd_StaleBinaryInstallsOnConfirm(t *testing.T) {
	var calls [][]string
	cmd := testLintCmdWithDeps(recordingLintRunner(&calls), fakeLintLocator(
		golangciLint{path: "/usr/local/bin/golangci-lint", problem: staleLintProblem},
		golangciLint{path: "/gobin/golangci-lint"},
	))

	out, err := runLintCmd(t, cmd, "\n")

	require.NoError(t, err)
	assert.Contains(t, out, "/usr/local/bin/golangci-lint "+staleLintProblem)
	assert.Contains(t, out, "[Y/n]")
	assert.Equal(t, [][]string{
		{"make", "golangci-lint-install"},
		{"/gobin/golangci-lint", "run"},
	}, calls)
}

func TestTestLintCmd_YesFlagInstallsWithoutPrompt(t *testing.T) {
	var calls [][]string
	cmd := testLintCmdWithDeps(recordingLintRunner(&calls), fakeLintLocator(
		golangciLint{problem: "is not installed"},
		golangciLint{path: "/gobin/golangci-lint"},
	))

	out, err := runLintCmd(t, cmd, "", "--yes")

	require.NoError(t, err)
	assert.NotContains(t, out, "[Y/n]")
	assert.Equal(t, [][]string{
		{"make", "golangci-lint-install"},
		{"/gobin/golangci-lint", "run"},
	}, calls)
}

func TestTestLintCmd_DeclinedOrNoInputDoesNotInstall(t *testing.T) {
	for name, stdin := range map[string]string{"declined": "n\n", "no input": ""} {
		t.Run(name, func(t *testing.T) {
			var calls [][]string
			cmd := testLintCmdWithDeps(recordingLintRunner(&calls), fakeLintLocator(
				golangciLint{path: "/usr/local/bin/golangci-lint", problem: staleLintProblem},
			))

			_, err := runLintCmd(t, cmd, stdin)

			require.ErrorIs(t, err, constants.ErrLintingFailed)
			assert.Contains(t, err.Error(), "make golangci-lint-install")
			assert.Empty(t, calls)
		})
	}
}

func TestTestLintCmd_StillStaleAfterInstallFails(t *testing.T) {
	var calls [][]string
	cmd := testLintCmdWithDeps(recordingLintRunner(&calls), fakeLintLocator(
		golangciLint{path: "/gobin/golangci-lint", problem: staleLintProblem},
	))

	_, err := runLintCmd(t, cmd, "y\n")

	require.ErrorIs(t, err, constants.ErrLintingFailed)
	assert.Contains(t, err.Error(), "still "+staleLintProblem)
	assert.Equal(t, [][]string{{"make", "golangci-lint-install"}}, calls)
}

func TestLintGoVersionProblem(t *testing.T) {
	assert.Equal(t, staleLintProblem, lintGoVersionProblem("go1.26.2", "1.27.3"))
	assert.Equal(t, "was built with go1.27.2, older than go1.27.3 in go.mod", lintGoVersionProblem("go1.27.2", "1.27.3"))
	assert.Empty(t, lintGoVersionProblem("go1.27.3", "1.27.3"))
	assert.Empty(t, lintGoVersionProblem("go1.28.0 X:nocoverageredesign", "1.27.3"))
	assert.Empty(t, lintGoVersionProblem("go1.26.2", ""))
}

func TestGoModGoVersion(t *testing.T) {
	assert.Equal(t, "1.27.3", goModGoVersion([]byte("module example.com/m\n\ngo 1.27.3\n\ntoolchain go1.27.4\n")))
	assert.Empty(t, goModGoVersion([]byte("module example.com/m\n")))
}

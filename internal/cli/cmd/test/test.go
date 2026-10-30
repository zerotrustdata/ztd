// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package testcmd

import (
	"bufio"
	"context"
	"debug/buildinfo"
	"fmt"
	"go/version"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/storage/storagetest"
)

type fileSvcFactory func(string, *slog.Logger) (fs.RuntimeFileService, error)

func Cmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "test",
		Aliases: []string{"tests"},
		Short:   "Run test suites and release qualification commands",
		Long:    `Run different tiers of the ztd test suite. Unit tests run fast without external dependencies. Integration tests use in-memory components. E2E tests require a running gateway. Lint runs static analysis. Chaos generates governance events for testing.`,
	}

	cmd.AddCommand(
		testUnitCmd(),
		testIntegrationCmd(),
		testE2ECmd(),
		testCoverageCmd(),
		testLintCmd(),
		publicLoopCmd(),
		chaosCmd(),
		scaleCmd(),
		testSummaryCmd(),
		releaseSmokeCmd(),
		testEvalLoopCmd(),
		testConformanceLoopCmd(),
	)

	return cmd
}

func testUnitCmd() *cobra.Command {
	return testUnitCmdWithRunner(realE2ERunner(os.Stdout, os.Stderr))
}

func testUnitCmdWithRunner(runner e2eCommandRunner) *cobra.Command {
	var runRegexp string
	var pkg string

	cmd := &cobra.Command{
		Use:   "unit",
		Short: "Run Tier 1 (Unit) tests",
		Long:  `Run unit tests without any build tags. These tests use mocks/stubs and have no external dependencies (no files, network, or DB). Use --pkg and --run to select a package and tests by regular expression.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !cmd.Flags().Changed("pkg") && !cmd.Flags().Changed("run") {
				fmt.Println("make test-unit")

				// Delegate to the Makefile so the CLI and repository test target share
				// package exclusions, race settings, cache settings, and timeouts.
				code, err := runner(cmd.Context(), "make", "test-unit")
				if err != nil {
					return fmt.Errorf("%w: %w", constants.ErrUnitTestsFailed, err)
				}
				if code != 0 {
					return fmt.Errorf("%w: exit code %d", constants.ErrUnitTestsFailed, code)
				}

				fmt.Println("Unit tests completed successfully.")
				return nil
			}

			fmt.Println("Running Tier 1 (Unit) tests...")

			testArgs := []string{"test", "-timeout", "180s"}
			if runRegexp != "" {
				testArgs = append(testArgs, "-run", runRegexp)
			}
			targetPkg := pkg
			if targetPkg == "" {
				targetPkg = "./..."
			}
			testArgs = append(testArgs, targetPkg)

			code, err := runner(cmd.Context(), "go", testArgs...)
			if err != nil {
				return fmt.Errorf("%w: %w", constants.ErrUnitTestsFailed, err)
			}
			if code != 0 {
				return fmt.Errorf("%w: exit code %d", constants.ErrUnitTestsFailed, code)
			}

			fmt.Println("Unit tests completed successfully.")
			return nil
		},
	}

	cmd.Flags().StringVar(&pkg, "pkg", "./...", "Package pattern selecting which unit test package to compile and run")
	cmd.Flags().StringVar(&runRegexp, "run", "", "Regular expression selecting which unit tests to run (passed to go test -run)")

	return cmd
}

func testIntegrationCmd() *cobra.Command {
	return testIntegrationCmdWithRunner(realE2ERunner(os.Stdout, os.Stderr))
}

func testIntegrationCmdWithRunner(runner e2eCommandRunner) *cobra.Command {
	var runRegexp string
	var pkg string
	var verbose bool

	cmd := &cobra.Command{
		Use:   "integration",
		Short: "Run Tier 2 (In-Process Integration) tests",
		Long:  `Run in-process integration tests with the 'integration' build tag. These tests run the gateway in-process against real on-disk SQLite databases, local PKI generation, and local pubsub. Use --pkg and --run to select a package and tests by regular expression.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !cmd.Flags().Changed("pkg") && !cmd.Flags().Changed("run") {
				target := "test-integration"
				fmt.Println("make " + target)

				// Delegate to the Makefile so the CLI and repository test target share
				// package exclusions, race settings, cache settings, and timeouts.
				code, err := runner(cmd.Context(), "make", target)
				if err != nil {
					return fmt.Errorf("%w: %w", constants.ErrIntegrationTestsFailed, err)
				}
				if code != 0 {
					return fmt.Errorf("%w: exit code %d", constants.ErrIntegrationTestsFailed, code)
				}

				fmt.Println("Integration tests completed successfully.")
				return nil
			}

			fmt.Println("Running Tier 2 (In-Process Integration) tests...")

			testArgs := []string{"test", "-tags=integration", "-count=1", "-timeout", integrationTestTimeout.String()}
			if verbose {
				testArgs = append(testArgs, "-v")
			}
			if runtime.GOOS != "windows" {
				testArgs = append(testArgs, "-race")
			}
			if runRegexp != "" {
				testArgs = append(testArgs, "-run", runRegexp)
			}
			targetPkg := pkg
			if targetPkg == "" {
				targetPkg = "./..."
			}
			testArgs = append(testArgs, targetPkg)

			code, err := runner(cmd.Context(), "go", testArgs...)
			if err != nil {
				return fmt.Errorf("%w: %w", constants.ErrIntegrationTestsFailed, err)
			}
			if code != 0 {
				return fmt.Errorf("%w: exit code %d", constants.ErrIntegrationTestsFailed, code)
			}

			fmt.Println("Integration tests completed successfully.")
			return nil
		},
	}

	cmd.Flags().StringVar(&pkg, "pkg", "./...", "Package pattern selecting which integration test package to compile and run")
	cmd.Flags().StringVar(&runRegexp, "run", "", "Regular expression selecting which integration tests to run (passed to go test -run)")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Show each integration test as it runs (passed to go test -v)")

	return cmd
}

// e2eCommandRunner abstracts exec.CommandContext so unit tests can verify
// argument propagation and failure wrapping without starting platform tests.
type e2eCommandRunner func(ctx context.Context, name string, args ...string) (int, error)

// integrationTestTimeout is the per-package Tier 2 deadline. It must match
// TEST_TIMEOUT in the Makefile, which CI uses.
const integrationTestTimeout = 600 * time.Second

// defaultE2ETimeout bounds the approved-stack suite. Long scenarios such as the
// Operator fleet soak raise it with --timeout.
const defaultE2ETimeout = 300 * time.Second

const defaultE2ERunRegexp = "^(TestApprovedRestart_|TestAuth_|TestCommandRoundtrip_|TestCompliance_|TestConsole_|TestGateway_|TestGovernance_|TestOperatorRegistry_|TestPlatform_FullBootstrap$|TestPubSub_|TestSSE_)"

// realE2ERunner runs the Go test binary as a child process, streaming stdout
// and stderr to the parent. It returns the child exit code and any start/run
// error. Cancellation via ctx signals the child process.
func realE2ERunner(stdout, stderr io.Writer) e2eCommandRunner {
	return func(ctx context.Context, name string, args ...string) (int, error) {
		c := exec.CommandContext(ctx, name, args...)
		c.Env = config.WithoutUserEnv(os.Environ())
		c.Stdout = stdout
		c.Stderr = stderr
		if err := c.Run(); err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				return exitErr.ExitCode(), err
			}
			return -1, err
		}
		return 0, nil
	}
}

func testE2ECmd() *cobra.Command {
	return testE2ECmdWithRunner(realE2ERunner(os.Stdout, os.Stderr))
}

// testE2ECmdWithRunner constructs the canonical Tier 3 executor. It runs only
// ./test/e2e/... with the e2e build tag, race detection on non-Windows
// platforms, -count=1, -parallel=1 as a backstop against accidental
// t.Parallel() use, and the approved-stack test set by default. An explicit
// --run regexp selects topology-specific scenarios. The external scenario runner
// owns stack lifecycle; this command performs network requests and assertions only.
func testE2ECmdWithRunner(runner e2eCommandRunner) *cobra.Command {
	var runRegexp string
	var timeout time.Duration

	cmd := &cobra.Command{
		Use:   "e2e",
		Short: "Run Tier 3 (Live Platform E2E) tests",
		Long: `Run Tier 3 (Live Platform E2E) tests against a running production platform.

Start the approved platform first, then run this command. The default suite
covers the approved-stack topology. Use --run to select a topology-specific
scenario such as pending enrollment, headless, denial, or cross-enrollment.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("Running Tier 3 (Live Platform E2E) tests...")

			testArgs := []string{"test", "-tags=e2e", "-count=1", "-parallel=1", "-timeout", timeout.String(), "-v"}
			if runtime.GOOS != "windows" {
				testArgs = append(testArgs, "-race")
			}
			if runRegexp == "" {
				runRegexp = defaultE2ERunRegexp
			}
			testArgs = append(testArgs, "-run", runRegexp, "./test/e2e/...")

			code, err := runner(cmd.Context(), "go", testArgs...)
			if err != nil {
				return fmt.Errorf("%w: %w", constants.ErrE2ETestsFailed, err)
			}
			if code != 0 {
				return fmt.Errorf("%w: exit code %d", constants.ErrE2ETestsFailed, code)
			}

			fmt.Println("E2E tests completed successfully.")
			return nil
		},
	}

	cmd.Flags().StringVar(&runRegexp, "run", "", "Regular expression selecting which E2E tests to run (passed to go test -run)")
	cmd.Flags().DurationVar(&timeout, "timeout", defaultE2ETimeout, "Overall time limit for the E2E run (passed to go test -timeout); raise it for long soak scenarios")

	return cmd
}

type coverageCommandRunner func(name string, args ...string) error

func realCoverageRunner(name string, args ...string) error {
	coverageCmd := exec.Command(name, args...)
	coverageCmd.Env = config.WithoutUserEnv(os.Environ())
	coverageCmd.Stdout = os.Stdout
	coverageCmd.Stderr = os.Stderr
	return coverageCmd.Run()
}

func testCoverageCmd() *cobra.Command {
	return testCoverageCmdWithRunner(realCoverageRunner)
}

func testCoverageCmdWithRunner(runner coverageCommandRunner) *cobra.Command {
	var pkg string
	var verbose bool

	cmd := &cobra.Command{
		Use:   "coverage",
		Short: "Run tests with coverage report",
		Long:  `Run tests with coverage profiling and enforce a minimum coverage threshold (75%). Use PKG flag to test a specific package, VERBOSE for detailed output.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("Running tests with coverage...")

			// Build the canonical Makefile coverage command.
			makeArgs := []string{"test-coverage"}

			// Forward package and verbosity selections as Make variables.
			if pkg != "" {
				makeArgs = append(makeArgs, "PKG="+pkg)
			}
			if verbose {
				makeArgs = append(makeArgs, "VERBOSE=1")
			}

			// Calculate coverage with the repository's canonical exclusions.
			if err := runner("make", makeArgs...); err != nil {
				return fmt.Errorf("%w: %w", constants.ErrCoverageTestsFailed, err)
			}

			// The Makefile target returns success only after enforcing the threshold.
			return nil
		},
	}

	cmd.Flags().StringVar(&pkg, "pkg", "", "Specific package to test (e.g., ./internal/services/auth)")
	cmd.Flags().BoolVar(&verbose, "verbose", false, "Verbose output")

	return cmd
}

func testLintCmd() *cobra.Command {
	return testLintCmdWithDeps(realE2ERunner(os.Stdout, os.Stderr), locateGolangciLint)
}

// golangciLint is the golangci-lint binary the lint command would run.
type golangciLint struct {
	path string
	// problem says why the binary cannot lint this module; empty when it can.
	problem string
	// shadowed is an earlier PATH entry that path was chosen over.
	shadowed string
}

type golangciLintLocator func(ctx context.Context) golangciLint

func testLintCmdWithDeps(runner e2eCommandRunner, locate golangciLintLocator) *cobra.Command {
	var assumeYes bool

	cmd := &cobra.Command{
		Use:   "lint",
		Short: "Run linting and quality checks",
		Long:  `Run golangci-lint with the repository configuration. This includes staticcheck, govet, and additional linters for bug prevention, security, and code quality. The golangci-lint in the Go bin directory is preferred over PATH, as in the Makefile. When it is missing or was built with a Go older than go.mod targets, the command offers to install the pinned version with make golangci-lint-install; --yes installs without asking.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("Running linting and quality checks...")

			lint := locate(cmd.Context())
			if lint.shadowed != "" {
				cmd.Printf("Using %s instead of %s, which is earlier on PATH.\n", lint.path, lint.shadowed)
			}
			if lint.problem != "" {
				cmd.Printf("%s %s.\n", lintDisplayName(lint), lint.problem)
				prompt := "Install the pinned golangci-lint now (make golangci-lint-install)?"
				if !assumeYes && !shared.AskYesNo(cmd, bufio.NewReader(cmd.InOrStdin()), prompt, true) {
					return fmt.Errorf("%w: golangci-lint %s (run make golangci-lint-install)", constants.ErrLintingFailed, lint.problem)
				}
				if _, err := runner(cmd.Context(), "make", "golangci-lint-install"); err != nil {
					return fmt.Errorf("%w: install golangci-lint: %w", constants.ErrLintingFailed, err)
				}
				// Re-resolve: the install lands in the Go bin dir, which locate prefers.
				if lint = locate(cmd.Context()); lint.problem != "" {
					return fmt.Errorf("%w: %s still %s after make golangci-lint-install", constants.ErrLintingFailed, lintDisplayName(lint), lint.problem)
				}
			}

			if _, err := runner(cmd.Context(), lint.path, "run"); err != nil {
				return fmt.Errorf("%w: %w", constants.ErrLintingFailed, err)
			}

			fmt.Println("Linting completed successfully.")
			return nil
		},
	}

	cmd.Flags().BoolVar(&assumeYes, shared.FlagYes, false, "Install the pinned golangci-lint without asking when it is missing or stale")

	return cmd
}

func lintDisplayName(lint golangciLint) string {
	if lint.path == "" {
		return "golangci-lint"
	}
	return lint.path
}

// locateGolangciLint resolves golangci-lint the way the Makefile does: the Go
// bin dir, where make golangci-lint-install puts the pinned build, ahead of PATH.
func locateGolangciLint(ctx context.Context) golangciLint {
	onPath, _ := exec.LookPath("golangci-lint")
	lint := golangciLint{path: onPath}

	if dir := goBinDir(ctx); dir != "" {
		name := "golangci-lint"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil {
			if onPath != "" {
				if pathInfo, err := os.Stat(onPath); err == nil && !os.SameFile(info, pathInfo) {
					lint.shadowed = onPath
				}
			}
			lint.path = candidate
		}
	}

	if lint.path == "" {
		lint.problem = "is not installed"
		return lint
	}
	built, err := buildinfo.ReadFile(lint.path)
	if err != nil {
		lint.problem = "has no readable Go build info"
		return lint
	}
	goMod, err := os.ReadFile("go.mod")
	if err != nil {
		// Outside the module root; golangci-lint reports its own errors.
		return lint
	}
	lint.problem = lintGoVersionProblem(built.GoVersion, goModGoVersion(goMod))
	return lint
}

// goBinDir is where go install writes: GOBIN, else the first GOPATH entry's bin.
func goBinDir(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "go", "env", "GOBIN", "GOPATH").Output()
	if err != nil {
		return ""
	}
	lines := strings.Split(string(out), "\n")
	if len(lines) < 2 {
		return ""
	}
	if gobin := strings.TrimSpace(lines[0]); gobin != "" {
		return gobin
	}
	if gopath := filepath.SplitList(strings.TrimSpace(lines[1])); len(gopath) > 0 && gopath[0] != "" {
		return filepath.Join(gopath[0], "bin")
	}
	return ""
}

// goModGoVersion returns the go directive of a go.mod file, or "" if it has none.
func goModGoVersion(goMod []byte) string {
	for _, line := range strings.Split(string(goMod), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "go "); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// lintGoVersionProblem reports why a golangci-lint built with builtWith (for
// example "go1.26.2") cannot load a module whose go directive is target, or ""
// when it can. golangci-lint refuses to run in that case.
func lintGoVersionProblem(builtWith, target string) string {
	if target == "" {
		return ""
	}
	built, _, _ := strings.Cut(builtWith, " ")
	if version.Compare(built, "go"+target) < 0 {
		return fmt.Sprintf("was built with %s, older than go%s in go.mod", built, target)
	}
	return ""
}

func testSummaryCmd() *cobra.Command {
	return testSummaryCmdWithConfig(shared.NewFileSvc)
}

func testSummaryCmdWithConfig(newFileSvc fileSvcFactory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "summary",
		Short: "View chaos test summary from test vault",
		Long:  `View aggregated chaos test results from the test vault database. This queries the chaos_events table across all test runs in the test vault directory.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			fileSvc, err := newFileSvc("", nil)
			if err != nil {
				return fmt.Errorf("%w: %w", constants.ErrFileServiceInit, err)
			}
			testVaultRelDir := constants.TestVaultDirname
			exists, err := fileSvc.FileExists(cmd.Context(), testVaultRelDir)
			if err != nil {
				return fmt.Errorf("%w: %w", constants.ErrDirectoryRead, err)
			}
			if !exists {
				cmd.Printf("Test vault directory not found at %s\n", fileSvc.Resolve(testVaultRelDir))
				cmd.Println("Generate test data first with:")
				cmd.Println("./ztd test chaos")
				return nil
			}

			entries, err := fileSvc.ReadDir(cmd.Context(), testVaultRelDir)
			if err != nil {
				return fmt.Errorf("%w: %w", constants.ErrDirectoryRead, err)
			}

			var testRuns []string
			for _, entry := range entries {
				if entry.IsDir() && strings.HasSuffix(entry.Name(), "chaos-test") {
					testRuns = append(testRuns, filepath.Join(testVaultRelDir, entry.Name()))
				}
			}

			if len(testRuns) == 0 {
				cmd.Println("No chaos test runs found in test vault.")
				cmd.Println("Generate test data first with:")
				cmd.Println("./ztd test chaos")
				return nil
			}

			latestRun := testRuns[len(testRuns)-1]
			// Each chaos run is its own runtime root; read its audit DB through the
			// storagetest owner that created it.
			runFileSvc, err := newFileSvc(fileSvc.Resolve(latestRun), nil)
			if err != nil {
				return fmt.Errorf("%w: %w", constants.ErrFileServiceInit, err)
			}
			exists, err = storagetest.ChaosRunDatabaseExists(cmd.Context(), runFileSvc)
			if err != nil {
				return fmt.Errorf("%w: %w", constants.ErrDirectoryRead, err)
			}
			if !exists {
				return fmt.Errorf("%w: %s", constants.ErrChaosTestDatabaseNotFound, runFileSvc.Resolve(constants.DataDirname))
			}

			results, err := storagetest.ReadChaosEventSummary(cmd.Context(), runFileSvc, nil)
			if err != nil {
				return err
			}
			total := 0
			for _, r := range results {
				total += r.Count
			}

			if total == 0 {
				cmd.Println("No chaos events found in database.")
				return nil
			}

			cmd.Printf("Chaos Test Summary (from: %s)\n", latestRun)
			cmd.Println(strings.Repeat("=", 110))
			cmd.Printf("%-23s | %-16s | %6s\n", "Category", "Outcome", "Count")
			cmd.Println(strings.Repeat("-", 110))
			for _, r := range results {
				cmd.Printf("%-23s | %-16s | %6d\n", r.Category, r.Outcome, r.Count)
			}
			cmd.Println(strings.Repeat("=", 110))
			cmd.Printf("%-23s | %-16s | %6d\n", "TOTAL", "", total)

			return nil
		},
	}

	return cmd
}

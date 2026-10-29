// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package platform

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/zerotrustdata/ztd/internal/cli/serve"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/httpclient"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/netutil"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/logging"
)

const (
	ShutdownTimeout     = 10 * time.Second
	HealthCheckInterval = 500 * time.Millisecond
	MaxHealthChecks     = 20
	MaxPortAttempts     = 100
	SigKillExitTimeout  = 2 * time.Second
)

// CommandExecutor defines an interface for executing external commands.
// It uses *exec.Cmd which is cross-platform.
type CommandExecutor interface {
	Command(name string, args ...string) *exec.Cmd
	Output(cmd *exec.Cmd) ([]byte, error)
	Run(cmd *exec.Cmd) error
}

// WindowsProcessChecker defines an interface for Windows-specific process checks.
// It uses uintptr for handles to remain cross-platform compatible.
type WindowsProcessChecker interface {
	OpenProcess(desiredAccess uint32, inheritHandle bool, processID uint32) (uintptr, error)
	CloseHandle(handle uintptr) error
	GetExitCodeProcess(handle uintptr, exitCode *uint32) error
}

// OperatorStartOptions holds configuration for starting the operator process.
// It embeds serve.GatewayConfig so that all gateway fields are shared in a
// single struct definition, eliminating the previous triple-duplication.
type OperatorStartOptions struct {
	serve.GatewayConfig
	// Notices receives progress the user must act on during a background
	// start, such as an OS key store approval prompt. Nil discards it.
	Notices io.Writer
}

// keyStoreApprovalNotice tells the user why a background start is still
// waiting after the child logged constants.KeyStoreAwaitingApprovalLog.
const keyStoreApprovalNotice = "[ztd] Waiting for access to the vault master key in the OS key store.\n" +
	"[ztd] On macOS, the Keychain dialog for the ztd-platform item wants your Mac login password, not a ztd account. Choose Always Allow.\n" +
	"[ztd] The dialog only appears in a GUI session. SSH cannot show it."

type ProcessManager struct {
	fileSvc fs.RuntimeFileService
	logSvc  *logging.LogService
	// findOperatorProcessFn allows mocking for tests
	findOperatorProcessFn func() int
	// Windows-specific dependencies for testing (used in process_windows.go)
	//nolint:unused // Used in platform-specific files and tests
	windowsProcessChecker WindowsProcessChecker
	//nolint:unused // Used in platform-specific files and tests
	commandExecutor CommandExecutor
	// isProcessRunningFn allows mocking for tests (used in process_windows.go)
	//nolint:unused // Used in platform-specific files and tests
	isProcessRunningFn  func(pid int) bool
	healthCheckInterval time.Duration
	maxHealthChecks     int
}

// SetHealthCheckParameters overrides the health check polling interval and max check count (primarily for tests).
func (pm *ProcessManager) SetHealthCheckParameters(interval time.Duration, maxChecks int) {
	pm.healthCheckInterval = interval
	pm.maxHealthChecks = maxChecks
}

func NewProcessManager(fileSvc fs.RuntimeFileService) (*ProcessManager, error) {
	if fileSvc == nil {
		return nil, fmt.Errorf("%w: fileSvc is required", constants.ErrPathValidation)
	}

	return &ProcessManager{
		fileSvc: fileSvc,
		logSvc:  logging.NewLogService(fileSvc),
	}, nil
}

// LogService returns the ProcessManager's LogService. Used by the ztd logs
// command to check log file existence and open the log for read via the
// file service rather than raw os calls.
func (pm *ProcessManager) LogService() *logging.LogService {
	return pm.logSvc
}

func (pm *ProcessManager) CreateDirectories() error {
	return pm.fileSvc.CreateRuntimeTree(context.Background())
}

func (pm *ProcessManager) WriteNetworkIdentityFile(identityData []byte) (string, error) {
	relPath := constants.NetworkIdentityFilename
	if err := pm.fileSvc.WriteFile(context.Background(), relPath, identityData, constants.PermFilePrivate); err != nil {
		return "", fmt.Errorf("%w: %w", constants.ErrFileWriteFailed, err)
	}
	return pm.fileSvc.Resolve(relPath), nil
}

func (pm *ProcessManager) checkPortAvailable(port int, name string) error {
	if err := netutil.CheckTCPPortAvailable(port); err != nil {
		return fmt.Errorf("port %d (%s): %w", port, name, err)
	}
	return nil
}

func (pm *ProcessManager) findAvailablePort(startPort int, name string) (int, error) {
	pid, err := pm.readPID(constants.OperatorPIDFilename)
	if err != nil {
		return 0, fmt.Errorf("%w: %w", constants.ErrPIDReadFailed, err)
	}

	if pid != 0 && !pm.isProcessRunning(pid) {
		if err := pm.deletePID(constants.OperatorPIDFilename); err != nil {
			return 0, fmt.Errorf("%w: pid %d: %w", constants.ErrPathValidation, pid, err)
		}
	}

	for attempt := 0; attempt < MaxPortAttempts; attempt++ {
		port := startPort + attempt
		// The gateway child binds these itself after start; probing them now
		// would succeed and then collide (e.g. 8080 busy -> 8081, which is the
		// public spectator ingest listener) and the health check would hit
		// the wrong listener.
		if _, reserved := constants.GatewayReservedLoopbackPorts[port]; reserved {
			continue
		}
		if err := netutil.CheckTCPPortAvailable(port); err == nil {
			return port, nil
		}

		conflictingPID := pm.findProcessOnPort(port)
		if conflictingPID > 0 && conflictingPID == pid {
			return 0, fmt.Errorf("%w: port %d (%s) by process %d", constants.ErrPortUnavailable, port, name, conflictingPID)
		}
	}

	return 0, fmt.Errorf("%w: starting from %d after %d attempts", constants.ErrPortUnavailable, startPort, MaxPortAttempts)
}

// ReadPIDFile reads a PID from the runtime pid directory.
func (pm *ProcessManager) ReadPIDFile(filename string) (int, error) {
	return pm.readPID(filename)
}

// WritePIDFile writes a PID into the runtime pid directory.
func (pm *ProcessManager) WritePIDFile(filename string, pid int) error {
	return pm.writePID(filename, pid)
}

// DeletePIDFile removes a PID file from the runtime pid directory.
func (pm *ProcessManager) DeletePIDFile(filename string) error {
	return pm.deletePID(filename)
}

// IsProcessRunning reports whether the given PID is alive.
func (pm *ProcessManager) IsProcessRunning(pid int) bool {
	return pm.isProcessRunning(pid)
}

func (pm *ProcessManager) readPID(filename string) (int, error) {
	relPath := filepath.Join(constants.PidDirname, filename)
	pidData, err := pm.fileSvc.ReadFile(context.Background(), relPath)
	if err != nil {
		if errors.Is(err, constants.ErrNotFound) {
			return 0, nil
		}
		return 0, fmt.Errorf("%w: %w", constants.ErrPIDReadFailed, err)
	}

	var pid int
	if _, err := fmt.Sscanf(string(pidData), "%d", &pid); err != nil {
		return 0, fmt.Errorf("%w: %w", constants.ErrPIDReadFailed, err)
	}

	return pid, nil
}

func (pm *ProcessManager) writePID(filename string, pid int) error {
	relPath := filepath.Join(constants.PidDirname, filename)
	return pm.fileSvc.WriteFile(context.Background(), relPath, []byte(strconv.Itoa(pid)), constants.PermFilePrivate)
}

func (pm *ProcessManager) deletePID(filename string) error {
	relPath := filepath.Join(constants.PidDirname, filename)
	return pm.fileSvc.Remove(context.Background(), relPath)
}

// operatorBinaryName returns the canonical filename for the copied operator
// binary in .ztd/bin, accounting for the platform-specific extension.
func operatorBinaryName() string {
	if runtime.GOOS == "windows" {
		return constants.BinaryImageNameWindows
	}
	return constants.BinaryImageName
}

// copyBinaryToBinDir copies the currently running executable into .ztd/bin
// and returns the absolute path to the copy. This gives the re-executed
// gateway process a stable binary location that survives rebuilds or moves
// of the original executable.
func (pm *ProcessManager) copyBinaryToBinDir() (string, error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("%w: %w", constants.ErrBinaryResolveFailed, err)
	}

	relPath := filepath.Join(constants.BinDirname, operatorBinaryName())
	destPath := pm.fileSvc.Resolve(relPath)

	src, err := os.Open(exePath)
	if err != nil {
		return "", fmt.Errorf("%w: %w", constants.ErrBinaryCopyFailed, err)
	}
	data, err := io.ReadAll(src)
	closeErr := src.Close()
	if err != nil {
		return "", fmt.Errorf("%w: read executable: %w", constants.ErrBinaryCopyFailed, err)
	}
	if closeErr != nil {
		return "", fmt.Errorf("%w: close executable: %w", constants.ErrBinaryCopyFailed, closeErr)
	}
	existing, readErr := pm.fileSvc.ReadFile(context.Background(), relPath)
	if readErr == nil && bytes.Equal(existing, data) {
		return destPath, nil
	}
	if readErr != nil && !errors.Is(readErr, constants.ErrNotFound) {
		return "", fmt.Errorf("%w: read runtime executable: %w", constants.ErrBinaryCopyFailed, readErr)
	}
	if err := pm.fileSvc.WriteFile(context.Background(), relPath, data, constants.PermFileExecutable); err != nil {
		return "", fmt.Errorf("%w: write runtime executable: %w", constants.ErrBinaryCopyFailed, err)
	}

	return destPath, nil
}

func (pm *ProcessManager) BuildReExecArgs(opts OperatorStartOptions) ([]string, error) {
	args := []string{
		"gw", "start", "--follow",
		"--posture", string(opts.Posture),
		"--data-dir", opts.DataDir,
		"--pki-dir", opts.PKIDir,
		"--secrets-dir", opts.SecretsDir,
		"--http-port", strconv.Itoa(opts.HTTPPort),
		"--https-port", strconv.Itoa(opts.HTTPSPort),
		"--log", opts.LogLevel,
	}

	if len(opts.OperatorCapabilities) > 0 {
		args = append(args, "--capabilities", opts.OperatorCapabilities.String())
	}

	if opts.InferenceOllamaEndpoint != "" {
		args = append(args, "--inference-ollama-endpoint", opts.InferenceOllamaEndpoint)
	}
	if opts.InferenceLlamaCppEndpoint != "" {
		args = append(args, "--inference-llamacpp-endpoint", opts.InferenceLlamaCppEndpoint)
	}
	if opts.InferenceLlamaCppModelRoot != "" {
		args = append(args, "--inference-llamacpp-model-root", opts.InferenceLlamaCppModelRoot)
	}
	if opts.InferenceKeepAlive != "" {
		args = append(args, "--inference-keep-alive", opts.InferenceKeepAlive)
	}
	if opts.ProviderBoundaryObserverID != "" {
		args = append(args, "--provider-boundary-observer-id", opts.ProviderBoundaryObserverID)
	}
	if opts.ProvenanceOperatorID != "" {
		args = append(args, "--provenance-operator-id", opts.ProvenanceOperatorID)
	}
	if opts.ProvenanceOperatorModelStorageRoot != "" {
		args = append(args, "--model-storage-root", opts.ProvenanceOperatorModelStorageRoot)
	}
	if opts.ProvenanceOperatorModelStorageFormat != "" {
		args = append(args, "--model-storage-format", opts.ProvenanceOperatorModelStorageFormat)
	}
	if opts.VaultDir != "" {
		args = append(args, "--vault-dir", opts.VaultDir)
	}

	if opts.CertIdentityMode != "" {
		args = append(args, "--cert-mode", opts.CertIdentityMode)
	}
	if opts.MCPDownstreamURL != "" {
		args = append(args, "--mcp-downstream-url", opts.MCPDownstreamURL)
	}
	if opts.MCPDownstreamCmd != "" {
		args = append(args, "--mcp-downstream-cmd", opts.MCPDownstreamCmd)
	}
	if len(opts.MCPDownstreamArgs) > 0 {
		args = append(args, "--mcp-downstream-args", strings.Join(opts.MCPDownstreamArgs, ","))
	}
	if opts.A2ADownstreamURL != "" {
		args = append(args, "--a2a-downstream-url", opts.A2ADownstreamURL)
	}
	if opts.PublicBaseURL != "" {
		args = append(args, "--public-base-url", opts.PublicBaseURL)
	}
	for _, origin := range opts.AllowedOrigins {
		args = append(args, "--cors-origin", origin)
	}

	if opts.PasskeyRpID != "" {
		args = append(args, "--passkey-rp-id", opts.PasskeyRpID)
	}
	if opts.PasskeyRpName != "" {
		args = append(args, "--passkey-rp-name", opts.PasskeyRpName)
	}
	for _, origin := range opts.PasskeyRpOrigins {
		args = append(args, "--passkey-rp-origin", origin)
	}
	if opts.RateLimitRPS > 0 {
		args = append(args, "--rate-limit-rps", fmt.Sprintf("%.1f", opts.RateLimitRPS))
	}
	if opts.RateLimitBurst > 0 {
		args = append(args, "--rate-limit-burst", strconv.Itoa(opts.RateLimitBurst))
	}
	if opts.DoctrineDir != "" {
		args = append(args, "--doctrine-dir", opts.DoctrineDir)
	}

	if opts.ListenHost != "" {
		args = append(args, "--listen-host", opts.ListenHost)
	}
	// Boolean flags have an implicit true value; a separate "false" token
	// becomes a positional argument instead of disabling the listeners.
	args = append(args, "--public-spectator="+strconv.FormatBool(opts.PublicSpectatorEnabled))
	if opts.PublicSpectatorPrivateAddr != "" {
		args = append(args, "--public-spectator-private-listen", opts.PublicSpectatorPrivateAddr)
	}
	if opts.PublicSpectatorPublicAddr != "" {
		args = append(args, "--public-spectator-public-listen", opts.PublicSpectatorPublicAddr)
	}
	if opts.PublicSpectatorAllowContainerBind {
		args = append(args, "--public-spectator-allow-container-bind")
	}
	if opts.EvalExplorerAddr != "" {
		args = append(args, "--eval-explorer-listen", opts.EvalExplorerAddr)
	}
	if opts.EvalExplorerRoot != "" {
		args = append(args, "--eval-explorer-root", opts.EvalExplorerRoot)
	}
	for _, cidr := range opts.PublicSpectatorTrustedProxyCIDRs {
		args = append(args, "--public-spectator-trusted-proxy-cidr", cidr)
	}
	if opts.PprofListenAddr != "" {
		args = append(args, "--pprof-listen", opts.PprofListenAddr)
	}

	return args, nil
}

func stopFailedStart(cmd *exec.Cmd) error {
	killErr := cmd.Process.Kill()
	_, waitErr := cmd.Process.Wait()
	if killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
		return fmt.Errorf("kill process: %w", killErr)
	}
	var exitErr *exec.ExitError
	if waitErr != nil && !errors.As(waitErr, &exitErr) && !errors.Is(waitErr, os.ErrProcessDone) {
		return fmt.Errorf("wait for process: %w", waitErr)
	}
	return nil
}

// maxHealthResponseBytes bounds the health-check response body read during
// start verification.
const maxHealthResponseBytes = 8192

// maxStartupDiagnosticBytes bounds the child output copied into a failed
// background-start error. The complete output remains in ztd.log.
const maxStartupDiagnosticBytes = 64 * 1024

// healthResponseIsFromChild reports whether a 200 health response was
// produced by the just-started child process. The gateway reports its own
// PID in the health body; a foreign listener on the same port must not
// satisfy the check.
func healthResponseIsFromChild(body io.Reader, childPID int) bool {
	var hr models.HealthResponse
	if err := json.NewDecoder(io.LimitReader(body, maxHealthResponseBytes)).Decode(&hr); err != nil {
		return false
	}
	return hr.PID != 0 && hr.PID == childPID
}

func (pm *ProcessManager) StartOperator(ctx context.Context, opts *OperatorStartOptions) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %w", constants.ErrProcessStartFailed, err)
	}
	if err := pm.CreateDirectories(); err != nil {
		return err
	}

	// Use provided values or defaults
	effectiveHTTPPort := opts.HTTPPort
	effectiveHTTPSPort := opts.HTTPSPort
	effectiveDataDir := opts.DataDir
	effectivePKIDir := opts.PKIDir
	effectiveSecretsDir := opts.SecretsDir
	effectiveVaultDir := opts.VaultDir
	effectivePasskeyRpID := opts.PasskeyRpID
	effectivePasskeyRpName := opts.PasskeyRpName

	// Normalize 127.0.0.1 to localhost for passkey RP ID
	// WebAuthn requires RP ID to be a valid domain, not an IP address
	if effectivePasskeyRpID == constants.LocalhostIP {
		effectivePasskeyRpID = constants.LocalhostHostname
	}
	effectiveRateLimitRPS := opts.RateLimitRPS
	effectiveRateLimitBurst := opts.RateLimitBurst
	effectiveLogLevel := opts.LogLevel

	// Use defaults if not provided
	if effectiveHTTPPort == 0 {
		effectiveHTTPPort = constants.Ports.OperatorHttp
	}
	if effectiveHTTPSPort == 0 {
		effectiveHTTPSPort = constants.Ports.OperatorHttps
	}
	if effectiveDataDir == "" {
		effectiveDataDir = pm.fileSvc.Resolve(constants.DataDirname)
	}
	if effectivePKIDir == "" {
		effectivePKIDir = pm.fileSvc.Resolve(constants.PkiDirname)
	}
	if effectiveSecretsDir == "" {
		effectiveSecretsDir = pm.fileSvc.Resolve(constants.SecretsDirname)
	}
	if effectiveLogLevel == "" {
		effectiveLogLevel = constants.LogLevelDefault
	}

	// Find the first available port starting from httpPort
	availableHTTPPort, err := pm.findAvailablePort(effectiveHTTPPort, "Operator HTTP")
	if err != nil {
		return fmt.Errorf("%w: HTTP port: %w", constants.ErrPortUnavailable, err)
	}

	// Calculate offset from original httpPort to maintain port spacing
	offset := availableHTTPPort - effectiveHTTPPort
	availableHTTPSPort := effectiveHTTPSPort + offset

	// Verify the calculated HTTPS port is available
	if err := pm.checkPortAvailable(availableHTTPSPort, "Operator HTTPS"); err != nil {
		return fmt.Errorf("%w: HTTPS port %d: %w", constants.ErrPortUnavailable, availableHTTPSPort, err)
	}
	if offset != 0 && opts.Notices != nil {
		holder := "another process"
		if holderPID := pm.findProcessOnPort(effectiveHTTPPort); holderPID > 0 {
			holder = fmt.Sprintf("PID %d", holderPID)
		}
		_, _ = fmt.Fprintf(opts.Notices,
			"[ztd] Port %d is held by %s; the Gateway listens on HTTP %d and HTTPS %d instead.\n"+
				"[ztd] CLI commands run from this directory follow these ports; Operators on other hosts need -e <host>:%d --port %d.\n",
			effectiveHTTPPort, holder, availableHTTPPort, availableHTTPSPort, availableHTTPPort, availableHTTPSPort)
	}
	if err := pm.checkSpectatorListenersFree(opts.GatewayConfig); err != nil {
		return err
	}

	binPath, err := pm.copyBinaryToBinDir()
	if err != nil {
		return err
	}

	logHandle, err := pm.logSvc.OpenLogForAppend(context.Background())
	if err != nil {
		return fmt.Errorf("%w: %w", constants.ErrPathValidation, err)
	}
	logPath := pm.logSvc.LogFilePath()
	logStartOffset, err := logHandle.Seek(0, io.SeekEnd)
	if err != nil {
		if closeErr := logHandle.Close(); closeErr != nil {
			return fmt.Errorf("%w: seek log: %w; close log: %w", constants.ErrProcessStartFailed, err, closeErr)
		}
		return fmt.Errorf("%w: seek log: %w", constants.ErrProcessStartFailed, err)
	}

	opts.HTTPPort = availableHTTPPort
	opts.HTTPSPort = availableHTTPSPort
	opts.DataDir = effectiveDataDir
	opts.PKIDir = effectivePKIDir
	opts.SecretsDir = effectiveSecretsDir
	opts.VaultDir = effectiveVaultDir
	opts.PasskeyRpID = effectivePasskeyRpID
	opts.PasskeyRpName = effectivePasskeyRpName
	opts.RateLimitRPS = effectiveRateLimitRPS
	opts.RateLimitBurst = effectiveRateLimitBurst
	opts.LogLevel = effectiveLogLevel

	args, err := pm.BuildReExecArgs(*opts)
	if err != nil {
		if closeErr := logHandle.Close(); closeErr != nil {
			return fmt.Errorf("%w: build arguments: %w; close log: %w", constants.ErrPathValidation, err, closeErr)
		}
		return err
	}

	cmd := exec.Command(binPath, args...)
	cmd.Stdout = logHandle
	cmd.Stderr = logHandle
	setProcessGroup(cmd)

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %w", constants.ErrProcessStartFailed, errors.Join(err, logHandle.Close()))
	}
	if err := cmd.Start(); err != nil {
		if closeErr := logHandle.Close(); closeErr != nil {
			return fmt.Errorf("%w: start: %w; close log: %w", constants.ErrProcessStartFailed, err, closeErr)
		}
		return fmt.Errorf("%w: %w", constants.ErrProcessStartFailed, err)
	}

	if err := pm.writePID(constants.OperatorPIDFilename, cmd.Process.Pid); err != nil {
		stopErr := stopFailedStart(cmd)
		if closeErr := logHandle.Close(); closeErr != nil {
			if stopErr != nil {
				return fmt.Errorf("%w: %w; stop failed start: %w; close log: %w", constants.ErrPIDWriteFailed, err, stopErr, closeErr)
			}
			return fmt.Errorf("%w: %w; close log: %w", constants.ErrPIDWriteFailed, err, closeErr)
		}
		if stopErr != nil {
			return fmt.Errorf("%w: %w; stop failed start: %w", constants.ErrPIDWriteFailed, err, stopErr)
		}
		return fmt.Errorf("%w: %w", constants.ErrPIDWriteFailed, err)
	}

	if err := logHandle.Close(); err != nil {
		cleanupErr := errors.Join(stopFailedStart(cmd), pm.deletePID(constants.OperatorPIDFilename))
		if cleanupErr != nil {
			return fmt.Errorf("%w: close log: %w; cleanup: %w", constants.ErrPathValidation, err, cleanupErr)
		}
		return fmt.Errorf("%w: close log: %w", constants.ErrPathValidation, err)
	}

	// Reap and observe the child directly: an exited child stays a zombie
	// until waited on, and signal-0 liveness checks report zombies as running.
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	// The goroutine above owns cmd.Wait, so cleanup must not call Wait again
	// (a second wait fails with ECHILD and masks the real start failure).
	stopChild := func(exited bool) error {
		if exited {
			return nil
		}
		if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return fmt.Errorf("kill process: %w", err)
		}
		<-waitCh
		return nil
	}

	failStart := func() error {
		cleanupErr := errors.Join(stopChild(true), pm.deletePID(constants.OperatorPIDFilename))
		diagnostic := pm.startupDiagnostic(logStartOffset)
		if cleanupErr != nil {
			if diagnostic != "" {
				return fmt.Errorf("%w: %s (full log: %s): cleanup: %w", constants.ErrProcessStartFailed, diagnostic, logPath, cleanupErr)
			}
			return fmt.Errorf("%w: check %s: cleanup: %w", constants.ErrProcessStartFailed, logPath, cleanupErr)
		}
		if diagnostic != "" {
			return fmt.Errorf("%w: %s (full log: %s)", constants.ErrProcessStartFailed, diagnostic, logPath)
		}
		return fmt.Errorf("%w: check %s", constants.ErrProcessStartFailed, logPath)
	}

	checkInterval := HealthCheckInterval
	if pm.healthCheckInterval > 0 {
		checkInterval = pm.healthCheckInterval
	}
	maxChecks := MaxHealthChecks
	if pm.maxHealthChecks > 0 {
		maxChecks = pm.maxHealthChecks
	}

	healthURL := fmt.Sprintf("http://%s:%d%s", constants.LocalhostIP, availableHTTPPort, constants.APIPaths.Health)
	client := &http.Client{
		Transport: httpclient.NewIPv4Transport(nil),
		Timeout:   checkInterval,
	}
	budget := checkInterval * time.Duration(maxChecks)
	if opts.PublicSpectatorEnabled && pm.maxHealthChecks <= 0 {
		budget += constants.PublicMirrorVerificationTimeout
	}
	healthCtx, cancelHealth := context.WithCancelCause(ctx)
	deadlineTimer := time.AfterFunc(budget, func() { cancelHealth(context.DeadlineExceeded) })
	defer func() {
		deadlineTimer.Stop()
		cancelHealth(nil)
	}()
	started := time.Now()
	approvalPending := false
healthLoop:
	for {
		select {
		case <-waitCh:
			return failStart()
		case <-healthCtx.Done():
			break healthLoop
		default:
		}
		req, err := http.NewRequestWithContext(healthCtx, http.MethodGet, healthURL, nil)
		if err != nil {
			cleanupErr := errors.Join(stopChild(false), pm.deletePID(constants.OperatorPIDFilename))
			return fmt.Errorf("%w: health request: %w", constants.ErrProcessStartFailed, errors.Join(err, cleanupErr))
		}
		resp, err := client.Do(req)
		if err == nil {
			healthy := resp.StatusCode == http.StatusOK &&
				healthResponseIsFromChild(resp.Body, cmd.Process.Pid)
			_ = resp.Body.Close()
			if healthy && healthCtx.Err() == nil {
				return nil
			}
		}
		// Key store approval has its own bounded allowance, followed by
		// the ordinary startup budget for verification and health.
		if !approvalPending && pm.childLogContains(logStartOffset, constants.KeyStoreAwaitingApprovalLog) {
			// Never extend a deadline that has already fired. One context
			// owns both phases; resetting its timer keeps cancellation and
			// cleanup independent of which phase is active.
			if !deadlineTimer.Stop() {
				cancelHealth(context.DeadlineExceeded)
				break healthLoop
			}
			approvalPending = true
			deadlineTimer.Reset(constants.KeyStoreApprovalTimeout + budget)
			if opts.Notices != nil {
				_, _ = fmt.Fprintln(opts.Notices, keyStoreApprovalNotice)
			}
		}
		timer := time.NewTimer(checkInterval)
		select {
		case <-waitCh:
			timer.Stop()
			return failStart()
		case <-healthCtx.Done():
			timer.Stop()
			break healthLoop
		case <-timer.C:
		}
	}

	reason := fmt.Sprintf("gateway did not become healthy after %v", time.Since(started).Round(time.Millisecond))
	if approvalPending {
		reason += ": OS key store approval or subsequent startup did not complete"
	}
	cleanupErr := errors.Join(stopChild(false), pm.deletePID(constants.OperatorPIDFilename))
	if diagnostic := pm.startupDiagnostic(logStartOffset); diagnostic != "" {
		reason += ": " + diagnostic
	}
	if cleanupErr != nil {
		return fmt.Errorf("%w: %s: %w (full log: %s); cleanup: %w", constants.ErrProcessStartFailed, reason, context.Cause(healthCtx), logPath, cleanupErr)
	}
	return fmt.Errorf("%w: %s: %w (full log: %s)", constants.ErrProcessStartFailed, reason, context.Cause(healthCtx), logPath)
}

// spectatorListenerPort is one fixed loopback listener the Gateway child binds
// when the public spectator is enabled.
type spectatorListenerPort struct {
	name string
	port int
}

// spectatorListenerPorts resolves the ports of the public spectator listeners:
// the mirror ingest, the mirror read, and the dedicated evaluation explorer
// (default 5173). It returns none when the spectator is disabled. A malformed
// listen address is rejected here, before any socket is probed.
func spectatorListenerPorts(cfg serve.GatewayConfig) ([]spectatorListenerPort, error) {
	if !cfg.PublicSpectatorEnabled {
		return nil, nil
	}
	listeners := []struct {
		addr        string
		defaultPort int
		name        string
	}{
		{cfg.PublicSpectatorPrivateAddr, constants.PublicSpectatorPrivatePort, "public spectator ingest"},
		{cfg.PublicSpectatorPublicAddr, constants.PublicSpectatorPublicPort, "public spectator"},
		{cfg.EvalExplorerAddr, constants.EvalExplorerDefaultPort, "evaluation explorer"},
	}
	ports := make([]spectatorListenerPort, 0, len(listeners))
	for _, l := range listeners {
		port := l.defaultPort
		if l.addr != "" {
			_, portStr, err := net.SplitHostPort(l.addr)
			if err != nil {
				return nil, fmt.Errorf("%w: %s listen address %q: %w", constants.ErrPortUnavailable, l.name, l.addr, err)
			}
			if port, err = strconv.Atoi(portStr); err != nil {
				return nil, fmt.Errorf("%w: %s listen address %q: %w", constants.ErrPortUnavailable, l.name, l.addr, err)
			}
		}
		ports = append(ports, spectatorListenerPort{name: l.name, port: port})
	}
	return ports, nil
}

// checkSpectatorListenersFree fails before the child is spawned when a fixed
// public spectator listener is already bound. Those ports sit outside the HTTP
// fallback search, so a conflict otherwise surfaces only as a bind error inside
// the child's log, and the holder is never named.
func (pm *ProcessManager) checkSpectatorListenersFree(cfg serve.GatewayConfig) error {
	ports, err := spectatorListenerPorts(cfg)
	if err != nil {
		return err
	}
	for _, l := range ports {
		if err := netutil.CheckTCPPortAvailable(l.port); err != nil {
			if holder := pm.findProcessOnPort(l.port); holder > 0 {
				return fmt.Errorf("%w: port %d (%s) is held by process %d; stop it or choose another listen address: %w",
					constants.ErrPortUnavailable, l.port, l.name, holder, err)
			}
			return fmt.Errorf("port %d (%s): %w", l.port, l.name, err)
		}
	}
	return nil
}

// startupDiagnostic returns the cause the child reported for this start attempt.
// The child logs a failure as an ERROR record followed by "  - key: value" field
// lines, so the record's error field is preferred over the trailing field line.
// It never reads output from an earlier invocation.
func (pm *ProcessManager) startupDiagnostic(startOffset int64) string {
	handle, err := pm.logSvc.OpenLogForRead(context.Background())
	if err != nil {
		return ""
	}
	defer handle.Close()
	end, err := handle.Seek(0, io.SeekEnd)
	if err != nil {
		return ""
	}
	if _, err := handle.Seek(max(startOffset, end-maxStartupDiagnosticBytes), io.SeekStart); err != nil {
		return ""
	}
	data, err := io.ReadAll(io.LimitReader(handle, maxStartupDiagnosticBytes))
	if err != nil {
		return ""
	}
	return diagnosticFromChildOutput(string(data))
}

// childLogContains reports whether the child's output for this start attempt
// contains marker. It never reads output from an earlier invocation.
func (pm *ProcessManager) childLogContains(startOffset int64, marker string) bool {
	handle, err := pm.logSvc.OpenLogForRead(context.Background())
	if err != nil {
		return false
	}
	defer handle.Close()
	if _, err := handle.Seek(startOffset, io.SeekStart); err != nil {
		return false
	}
	data, err := io.ReadAll(io.LimitReader(handle, maxStartupDiagnosticBytes))
	return err == nil && strings.Contains(string(data), marker)
}

// diagnosticFromChildOutput prefers the last ERROR record: its message plus the
// structured error field name the cause. Otherwise it falls back to the last
// record with its progress fields, or the last plain stderr line.
func diagnosticFromChildOutput(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		_, msg, isError := strings.Cut(lines[i], " ERROR: ")
		if !isError {
			continue
		}
		msg = strings.TrimSpace(msg)
		for _, field := range lines[i+1:] {
			if !strings.HasPrefix(field, "  - ") {
				break
			}
			if cause, ok := strings.CutPrefix(strings.TrimSpace(field), "- error: "); ok {
				return msg + ": " + strings.TrimSpace(cause)
			}
		}
		return msg
	}
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			start := i
			for start > 0 && strings.HasPrefix(lines[start], "  - ") {
				start--
			}
			return strings.TrimSpace(strings.Join(lines[start:i+1], "\n"))
		}
	}
	return ""
}

// StopOperator stops the Gateway that OperatorStatus reports, so a Gateway
// whose PID file was lost is still stopped rather than reported as stopped.
func (pm *ProcessManager) StopOperator() error {
	running, pid, err := pm.OperatorStatus()
	if err != nil {
		return err
	}
	if !running {
		return nil
	}

	if err := pm.stopProcess(pid, "operator"); err != nil {
		return fmt.Errorf("%w: %w", constants.ErrProcessStopFailed, err)
	}

	if err := pm.deletePID(constants.OperatorPIDFilename); err != nil {
		return err
	}

	return nil
}

func (pm *ProcessManager) OperatorStatus() (bool, int, error) {
	pid, err := pm.readPID(constants.OperatorPIDFilename)
	if err != nil {
		return false, 0, err
	}

	if pid != 0 {
		if pm.isProcessRunning(pid) {
			return true, pid, nil
		}
		// Stale PID file - clean it up and fall through to process discovery
		_ = pm.deletePID(constants.OperatorPIDFilename)
	}

	// PID file missing or stale, try to find the process
	if pm.findOperatorProcessFn != nil {
		pid = pm.findOperatorProcessFn()
	} else {
		pid = pm.findOperatorProcess()
	}
	if pid == 0 {
		return false, 0, nil
	}

	running := pm.isProcessRunning(pid)
	return running, pid, nil
}

func (pm *ProcessManager) GetLogPath() string {
	return pm.logSvc.LogFilePath()
}

// Clean stops the Operator and renames the runtime directory aside to
// .ztd-<MMDDHHMM> rather than deleting it. It returns the archive path, or ""
// when there was no runtime directory to archive.
func (pm *ProcessManager) Clean() (string, error) {
	if err := pm.StopOperator(); err != nil {
		return "", fmt.Errorf("%w: %w", constants.ErrProcessStopFailed, err)
	}

	archived, err := pm.fileSvc.ArchiveRuntime(context.Background(), time.Now())
	if err != nil {
		return "", fmt.Errorf("%w: runtime directory: %w", constants.ErrPathValidation, err)
	}

	return archived, nil
}

// TailLog prints a log file, optionally following new entries (like tail -f).
// The caller opens the file via the file service and passes the handle; the
// nonexistent-file case is handled by the caller through LogFileExists /
// OpenLogForRead returning constants.ErrNotFound.
func TailLog(r io.ReadSeeker, follow bool) error {
	// Print existing content
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("%w: seek to start: %w", constants.ErrDirectoryRead, err)
	}
	if _, err := io.Copy(os.Stdout, r); err != nil {
		return fmt.Errorf("%w: print content: %w", constants.ErrDirectoryRead, err)
	}

	if !follow {
		return nil
	}

	// Follow mode: seek to end and watch for new content
	if _, err := r.Seek(0, io.SeekEnd); err != nil {
		return fmt.Errorf("%w: seek to end: %w", constants.ErrDirectoryRead, err)
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigChan)

	// Use a goroutine to read lines and send to channel
	lineChan := make(chan string)
	errChan := make(chan error, 1)
	done := make(chan struct{})

	go func() {
		defer close(lineChan)
		reader := bufio.NewReader(r)
		for {
			select {
			case <-done:
				return
			default:
				line, err := reader.ReadString('\n')
				if err != nil {
					if err == io.EOF {
						time.Sleep(100 * time.Millisecond)
						continue
					}
					errChan <- fmt.Errorf("%w: read line: %w", constants.ErrDirectoryRead, err)
					return
				}
				lineChan <- line
			}
		}
	}()

	// Track parent PID to detect if parent has died
	parentPID := os.Getppid()

	for {
		select {
		case <-sigChan:
			// Exit gracefully on interrupt signal
			close(done)
			return nil
		case err := <-errChan:
			close(done)
			return err
		case line := <-lineChan:
			fmt.Print(line)
		case <-time.After(100 * time.Millisecond):
			// Check if parent process is still alive
			// If parent died (PID changed to 1 or process doesn't exist), exit
			currentParentPID := os.Getppid()
			if currentParentPID != parentPID {
				// Parent died, we were orphaned and adopted by init (PID 1)
				close(done)
				return nil
			}
		}
	}
}

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package operatordeploy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	sshlib "golang.org/x/crypto/ssh"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	sshpkg "github.com/zerotrustdata/ztd/internal/pkg/ssh"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/uuid"
)

const (
	// DirPrefix names the per-Operator directories created under --dest-dir
	// when --count is greater than one.
	DirPrefix = "op"
	// MaxParallel bounds concurrent staging to the Gateway's live Operator request quota.
	MaxParallel = constants.PlatformEnrollmentMaxLiveOperatorRequests

	DiagnoseTimeout   = 10 * time.Second
	OnlineBaseTimeout = time.Minute
	OnlinePerOperator = time.Second

	// ApprovalWindow bounds how many approved Operators may be between approval
	// and an announced command subscription at once.
	ApprovalWindow = 256
)

// DefaultEnrollTimeout bounds how long deploy waits for the Gateway to announce
// one worker's pending request. Exposed as a var so tests can shorten it.
var DefaultEnrollTimeout = 30 * time.Second

// DeploymentIDForExecution returns the deterministic RFC 4122 v5 UUID used
// as --deployment-id for the hostIndex-th target of one governed deploy call.
func DeploymentIDForExecution(executionID string, hostIndex int) string {
	return uuid.Format(uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("%s:%d", executionID, hostIndex))))
}

// RemoteDeployClient abstracts binary deployment and host agent execution for remote targets.
type RemoteDeployClient interface {
	IsWSL() bool
	UploadBinary(ctx context.Context, sourceBinary, remoteDir string) (string, error)
	ExecuteAgent(ctx context.Context, req models.DeployHostRequest) (models.DeployHostResponse, error)
	Close() error
}

// RemoteDeployClientFactory constructs a RemoteDeployClient for a remote host.
type RemoteDeployClientFactory func(ctx context.Context, host string, port int, identityFile string) (RemoteDeployClient, error)

// IsLocalTarget reports whether host names the local machine.
func IsLocalTarget(host string) bool {
	h := strings.TrimSpace(host)
	return h == "local" || strings.EqualFold(h, constants.LocalhostHostname) || h == "127.0.0.1"
}

// IsRemoteTarget reports whether host names a remote SSH target.
func IsRemoteTarget(host string) bool {
	return !IsLocalTarget(host)
}

// IsWindowsPath reports whether p is a Windows drive or UNC path.
func IsWindowsPath(p string) bool {
	p = strings.TrimSpace(p)
	if len(p) >= 3 && p[0] == '/' && ((p[1] >= 'a' && p[1] <= 'z') || (p[1] >= 'A' && p[1] <= 'Z')) && p[2] == ':' {
		return true
	}
	if len(p) >= 2 && ((p[0] >= 'a' && p[0] <= 'z') || (p[0] >= 'A' && p[0] <= 'Z')) && p[1] == ':' {
		return true
	}
	return strings.HasPrefix(p, `\\`) || strings.Contains(p, `\`)
}

// ToWindowsFilePath strips a leading slash before a Windows drive letter.
func ToWindowsFilePath(p string) string {
	p = strings.TrimSpace(p)
	if len(p) >= 3 && p[0] == '/' && ((p[1] >= 'a' && p[1] <= 'z') || (p[1] >= 'A' && p[1] <= 'Z')) && p[2] == ':' {
		return p[1:]
	}
	return p
}

// ResolveSFTPPath resolves a leading ~ or ~/ relative to the SFTP working directory.
func ResolveSFTPPath(wd, p string) string {
	p = strings.TrimSpace(p)
	if p == "~" {
		return wd
	}
	if strings.HasPrefix(p, "~/") {
		return path.Join(wd, strings.TrimPrefix(p, "~/"))
	}
	if strings.HasPrefix(p, `~\`) {
		return path.Join(wd, strings.TrimPrefix(p, `~\`))
	}
	return p
}

type sshRemoteDeployClient struct {
	client        *sshlib.Client
	sftpClient    *sftp.Client
	host          string
	isWSL         bool
	workingDir    string
	agentPath     string
	stopKeepalive func()
}

// DefaultRemoteDeployClientFactory dials a host over SSH and opens SFTP for remote staging.
func DefaultRemoteDeployClientFactory(ctx context.Context, host string, port int, identityFile string) (RemoteDeployClient, error) {
	var portStr string
	if port > 0 {
		portStr = strconv.Itoa(port)
	}
	r, err := sshpkg.ResolveHost(host, "", "", identityFile, "")
	if err != nil {
		return nil, fmt.Errorf("resolve host %s: %w", host, err)
	}
	if portStr != "" {
		r.Port = portStr
	}

	authSock := os.Getenv(string(constants.EnvVar.SSHAuthSock))
	authMethods, err := sshpkg.BuildAuthMethods(r, authSock, "")
	if err != nil {
		return nil, fmt.Errorf("build ssh auth for %s: %w", host, err)
	}
	if len(authMethods) == 0 {
		return nil, fmt.Errorf("no ssh auth methods available for %s", host)
	}

	hostKeyCB, err := sshpkg.BuildHostKeyCallback("")
	if err != nil {
		return nil, fmt.Errorf("ssh host key callback: %w", err)
	}

	clientConfig := &sshlib.ClientConfig{
		User:            r.User,
		Auth:            authMethods,
		HostKeyCallback: hostKeyCB,
		Timeout:         30 * time.Second,
	}

	addr := net.JoinHostPort(r.Hostname, r.Port)
	client, err := sshpkg.DialSSH(ctx, r, clientConfig, addr)
	if err != nil {
		return nil, fmt.Errorf("dial ssh %s: %w", host, err)
	}

	stopKeepalive := sshpkg.StartKeepalive(ctx, client, constants.SSHKeepaliveInterval, constants.SSHKeepaliveMaxMissed)

	rc, err := newSSHRemoteDeployClient(client, host, stopKeepalive)
	if err != nil {
		stopKeepalive()
		_ = client.Close()
		return nil, err
	}
	return rc, nil
}

// NewSSHRemoteDeployClient wraps an established SSH client with SFTP for remote deployment.
func NewSSHRemoteDeployClient(client *sshlib.Client, host string) (RemoteDeployClient, error) {
	return newSSHRemoteDeployClient(client, host, nil)
}

func newSSHRemoteDeployClient(client *sshlib.Client, host string, stopKeepalive func()) (*sshRemoteDeployClient, error) {
	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		return nil, fmt.Errorf("sftp client %s: %w", host, err)
	}

	wd, err := sftpClient.Getwd()
	if err != nil {
		wd = "."
	}

	return &sshRemoteDeployClient{
		client:        client,
		sftpClient:    sftpClient,
		host:          host,
		isWSL:         IsWindowsPath(wd),
		workingDir:    wd,
		stopKeepalive: stopKeepalive,
	}, nil
}

func (c *sshRemoteDeployClient) IsWSL() bool { return c.isWSL }

func (c *sshRemoteDeployClient) Close() error {
	if c.stopKeepalive != nil {
		c.stopKeepalive()
	}
	var errs []error
	if c.sftpClient != nil {
		if err := c.sftpClient.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if c.client != nil {
		if err := c.client.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (c *sshRemoteDeployClient) runAgentCommand(ctx context.Context, cmdStr string, req models.DeployHostRequest) (models.DeployHostResponse, error) {
	session, err := c.client.NewSession()
	if err != nil {
		return models.DeployHostResponse{}, fmt.Errorf("new ssh session on %s: %w", c.host, err)
	}
	defer session.Close()

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = session.Close()
		case <-done:
		}
	}()

	reqData, err := json.Marshal(req)
	if err != nil {
		return models.DeployHostResponse{}, fmt.Errorf("encode deploy-host request: %w", err)
	}

	var stdout, stderr bytes.Buffer
	session.Stdin = bytes.NewReader(reqData)
	session.Stdout = &stdout
	session.Stderr = &stderr

	if err := session.Run(cmdStr); err != nil {
		return models.DeployHostResponse{}, fmt.Errorf("exec deploy-host on %s: %w (stderr: %s)", c.host, err, strings.TrimSpace(stderr.String()))
	}

	var resp models.DeployHostResponse
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		return resp, fmt.Errorf("decode deploy-host response from %s: %w (stdout: %s)", c.host, err, strings.TrimSpace(stdout.String()))
	}
	if !resp.Success {
		return resp, fmt.Errorf("deploy-host on %s: %s", c.host, resp.Error)
	}
	return resp, nil
}

func (c *sshRemoteDeployClient) UploadBinary(ctx context.Context, sourceBinary, remoteDir string) (string, error) {
	if c.isWSL {
		windowsStagingDir := path.Join(c.workingDir, constants.DeployBinDirname)
		if err := c.sftpClient.MkdirAll(windowsStagingDir); err != nil {
			return "", fmt.Errorf("create windows staging dir on %s: %w", c.host, err)
		}
		stagingFile := path.Join(windowsStagingDir, "ztd.staging")

		srcFile, err := os.Open(sourceBinary)
		if err != nil {
			return "", fmt.Errorf("open source binary: %w", err)
		}
		defer srcFile.Close()

		dstFile, err := c.sftpClient.OpenFile(stagingFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
		if err != nil {
			return "", fmt.Errorf("create staging binary on %s: %w", c.host, err)
		}
		if _, err := io.Copy(dstFile, srcFile); err != nil {
			_ = dstFile.Close()
			return "", fmt.Errorf("upload staging binary to %s: %w", c.host, err)
		}
		if err := dstFile.Chmod(constants.PermFileExecutable); err != nil {
			_ = dstFile.Close()
			return "", fmt.Errorf("chmod staging binary on %s: %w", c.host, err)
		}
		if err := dstFile.Close(); err != nil {
			return "", fmt.Errorf("close staging binary on %s: %w", c.host, err)
		}

		bootstrapCmd := fmt.Sprintf("wsl.exe --cd %s -e ./ztd.staging operator deploy-host", ToWindowsFilePath(windowsStagingDir))
		bootstrapReq := models.DeployHostRequest{
			Action:  models.DeployHostActionInstall,
			Source:  "./ztd.staging",
			DestDir: remoteDir,
		}
		resp, err := c.runAgentCommand(ctx, bootstrapCmd, bootstrapReq)
		_ = c.sftpClient.Remove(stagingFile)
		if err != nil {
			return "", fmt.Errorf("%w: WSL bootstrap failed on %s: %v", constants.ErrWSLUnavailable, c.host, err)
		}
		if !resp.Success {
			return "", fmt.Errorf("%w: WSL bootstrap on %s: %s", constants.ErrWSLUnavailable, c.host, resp.Error)
		}

		targetBinDir := path.Join(remoteDir, constants.DeployBinDirname)
		c.agentPath = path.Join(targetBinDir, constants.BinaryImageName)
		return targetBinDir, nil
	}

	resolvedDestDir := ResolveSFTPPath(c.workingDir, remoteDir)
	targetBinDir := path.Join(resolvedDestDir, constants.DeployBinDirname)
	if err := c.sftpClient.MkdirAll(targetBinDir); err != nil {
		return "", fmt.Errorf("create %s on %s: %w", targetBinDir, c.host, err)
	}

	stagingPath := path.Join(targetBinDir, "ztd.new")
	srcFile, err := os.Open(sourceBinary)
	if err != nil {
		return "", fmt.Errorf("open source binary: %w", err)
	}
	defer srcFile.Close()

	dstFile, err := c.sftpClient.OpenFile(stagingPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return "", fmt.Errorf("create %s on %s: %w", stagingPath, c.host, err)
	}
	if _, err := io.Copy(dstFile, srcFile); err != nil {
		_ = dstFile.Close()
		return "", fmt.Errorf("upload binary to %s on %s: %w", stagingPath, c.host, err)
	}
	if err := dstFile.Chmod(constants.PermFileExecutable); err != nil {
		_ = dstFile.Close()
		return "", fmt.Errorf("chmod %s on %s: %w", stagingPath, c.host, err)
	}
	if err := dstFile.Close(); err != nil {
		return "", fmt.Errorf("close %s on %s: %w", stagingPath, c.host, err)
	}

	finalPath := path.Join(targetBinDir, constants.BinaryImageName)
	if err := c.sftpClient.PosixRename(stagingPath, finalPath); err != nil {
		_ = c.sftpClient.Remove(finalPath)
		if err := c.sftpClient.Rename(stagingPath, finalPath); err != nil {
			return "", fmt.Errorf("install binary on %s: %w", c.host, err)
		}
	}

	c.agentPath = finalPath
	return targetBinDir, nil
}

func (c *sshRemoteDeployClient) ExecuteAgent(ctx context.Context, req models.DeployHostRequest) (models.DeployHostResponse, error) {
	cmdStr := fmt.Sprintf("%s operator deploy-host", c.agentPath)
	if c.isWSL {
		cmdStr = fmt.Sprintf("wsl.exe -e %s operator deploy-host", c.agentPath)
	}
	resp, err := c.runAgentCommand(ctx, cmdStr, req)
	if err != nil {
		if c.isWSL {
			return resp, fmt.Errorf("%w: %v", constants.ErrWSLUnavailable, err)
		}
		return resp, err
	}
	return resp, nil
}

// DeployTarget is the lifecycle surface shared by process and SSH deployments.
// Enrollment deliberately lives above this boundary so every transport retains
// the same request-specific approval and readiness checks.
type DeployTarget interface {
	Name() string
	StartOperator(context.Context, string, string, ...string) error
	// ReadDeploymentState reads the worker's recorded progress once. Waiting is
	// event driven; this only explains a wait that failed.
	ReadDeploymentState(context.Context, string) (*models.OperatorDeploymentState, error)
	MarkReady(context.Context, string) error
}

// DeployedOperator is one Operator working directory that deploy set up.
type DeployedOperator struct {
	Target    DeployTarget
	Dir       string
	LaunchID  string
	RequestID string
	// Fingerprints are the keys the worker recorded; approval requires the
	// pending request to carry them (set only when ReadFingerprints is true).
	Fingerprints models.PlatformEnrollmentCSRFingerprints
	SessionID    string
	Watch        *DeploymentWatch
}

// HostTransport executes staging, preflight, and worker launch on one local or remote host.
type HostTransport struct {
	Host           string
	Port           int
	IdentityFile   string
	Local          bool
	BinaryDir      string
	LaunchID       string
	RemoteClient   RemoteDeployClient
	EnsureRecovery func(dir string) error
	LocalStartFunc func(ctx context.Context, dir, binary string, args []string) (int, error)
}

func (s HostTransport) Name() string { return s.Host }

func (s HostTransport) MarkReady(context.Context, string) error { return nil }

func (s HostTransport) BinaryName() string {
	if s.Local && runtime.GOOS == "windows" {
		return constants.BinaryImageNameWindows
	}
	return constants.BinaryImageName
}

// PrepareDir creates dir on the host and returns its absolute path.
func (s HostTransport) PrepareDir(ctx context.Context, dir string) (string, error) {
	if s.Local {
		dirs, err := ExecuteDeployHostPrepare(ctx, []string{dir})
		if err != nil {
			return "", err
		}
		return dirs[0], nil
	}
	resp, err := s.RemoteClient.ExecuteAgent(ctx, models.DeployHostRequest{
		Action: models.DeployHostActionPrepare,
		Dirs:   []string{dir},
	})
	if err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	if len(resp.ResolvedDirs) == 0 {
		return "", fmt.Errorf("prepare %s returned no resolved dirs", dir)
	}
	absDir := resp.ResolvedDirs[0]
	if !RemoteDirPattern.MatchString(absDir) {
		return "", fmt.Errorf("%w: resolved directory %q must match %s", constants.ErrPathValidation, absDir, RemoteDirPattern)
	}
	return absDir, nil
}

// InstallBinary uploads/copies or hard-links the binary into dir.
func (s HostTransport) InstallBinary(ctx context.Context, sourceBinary, dir string) error {
	binName := s.BinaryName()
	if s.Local {
		if s.BinaryDir != "" {
			return ExecuteDeployHostLink(ctx, s.BinaryDir, dir, binName)
		}
		target := filepath.Join(dir, binName)
		return ExecuteDeployHostInstall(ctx, sourceBinary, target)
	}
	_, err := s.RemoteClient.ExecuteAgent(ctx, models.DeployHostRequest{
		Action:    models.DeployHostActionLink,
		BinaryDir: s.BinaryDir,
		DestDir:   dir,
		Binary:    constants.BinaryImageName,
	})
	return err
}

// PreflightGateway runs gateway-preflight on this host.
func (s HostTransport) PreflightGateway(ctx context.Context, args []string) error {
	if s.Local {
		return ExecuteDeployHostPreflight(ctx, args)
	}
	_, err := s.RemoteClient.ExecuteAgent(ctx, models.DeployHostRequest{
		Action:        models.DeployHostActionPreflight,
		PreflightArgs: args,
	})
	return err
}

// StartOperator stops any previous worker in dir and starts a new one.
func (s HostTransport) StartOperator(ctx context.Context, dir, endpoint string, startArgs ...string) error {
	args := append([]string{"--endpoint", endpoint}, startArgs...)
	if s.Local {
		if s.EnsureRecovery != nil {
			if err := s.EnsureRecovery(dir); err != nil {
				return err
			}
		}
		if s.LocalStartFunc != nil {
			_, err := s.LocalStartFunc(ctx, dir, s.BinaryName(), args)
			return err
		}
		_, err := ExecuteDeployHostStart(ctx, dir, s.BinaryName(), args)
		return err
	}
	_, err := s.RemoteClient.ExecuteAgent(ctx, models.DeployHostRequest{
		Action:     models.DeployHostActionStart,
		WorkingDir: dir,
		Binary:     constants.BinaryImageName,
		Args:       args,
	})
	return err
}

// ReadDeploymentState reads the recorded OperatorDeploymentState for this launch.
func (s HostTransport) ReadDeploymentState(ctx context.Context, dir string) (*models.OperatorDeploymentState, error) {
	var state *models.OperatorDeploymentState
	if s.Local {
		fileSvc, err := fs.NewRuntimeFileService(dir, slog.Default())
		if err != nil {
			return nil, fmt.Errorf("%w: %w", constants.ErrFileServiceInit, err)
		}
		var readErr error
		state, readErr = ReadOperatorDeploymentState(ctx, fileSvc)
		if readErr != nil {
			return nil, readErr
		}
	} else {
		resp, err := s.RemoteClient.ExecuteAgent(ctx, models.DeployHostRequest{
			Action:     models.DeployHostActionState,
			WorkingDir: dir,
		})
		if err != nil {
			return nil, err
		}
		state = resp.State
	}
	if state != nil && state.LaunchID != s.LaunchID {
		return nil, nil
	}
	return state, nil
}

// StageOptions configures binary staging, worker startup, and event waiting.
type StageOptions struct {
	Endpoint         string
	Background       bool
	StartArgs        []string
	Preflight        []string
	ReadFingerprints bool
	Events           DeploymentWatcher
	EnrollTimeout    time.Duration
	LaunchIDFunc     func() (string, error)
}

// DeployOperatorResult is one buffered worker deployment outcome.
type DeployOperatorResult struct {
	Op     DeployedOperator
	Dir    string
	Output string
	Err    error
}

// ExplainDeployWait adds what the worker itself recorded to a failed wait.
func ExplainDeployWait(target DeployTarget, dir string, waitErr error) error {
	ctx, cancel := context.WithTimeout(context.Background(), DiagnoseTimeout)
	defer cancel()
	state, err := target.ReadDeploymentState(ctx, dir)
	switch {
	case err != nil:
		return fmt.Errorf("%w; Operator state: %w", waitErr, err)
	case state != nil && state.Phase == models.OperatorDeploymentPhaseFailed:
		return fmt.Errorf("%w; Operator reported: %s", waitErr, state.Error)
	}
	return waitErr
}

// AwaitStaged waits for the Gateway to announce the worker's pending request.
func AwaitStaged(ctx context.Context, watch *DeploymentWatch, target DeployTarget, dir string, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = DefaultEnrollTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	requestID, err := watch.AwaitStaged(ctx)
	if err != nil {
		return "", fmt.Errorf("await staged request in %s: %w", dir, ExplainDeployWait(target, dir, err))
	}
	return requestID, nil
}

// WorkerFingerprints reads the CSR fingerprints the worker recorded for this launch.
func WorkerFingerprints(ctx context.Context, target DeployTarget, dir string) (models.PlatformEnrollmentCSRFingerprints, error) {
	state, err := target.ReadDeploymentState(ctx, dir)
	if err != nil {
		return models.PlatformEnrollmentCSRFingerprints{}, fmt.Errorf("read Operator keys in %s: %w", dir, err)
	}
	if state == nil || state.Fingerprints.Operator == "" || state.Fingerprints.CLI == "" {
		return models.PlatformEnrollmentCSRFingerprints{}, fmt.Errorf("%w: Operator in %s recorded no enrollment keys for this launch", constants.ErrOperatorDeployFailed, dir)
	}
	return state.Fingerprints, nil
}

// GatewayPreflightError formats a reachable/loopback error for a failed preflight.
func GatewayPreflightError(target, endpoint string, err error) error {
	return fmt.Errorf("%w: %s cannot reach the Gateway at %s (it listens on loopback unless started with --listen-host 0.0.0.0): %w",
		constants.ErrOperatorDeployFailed, target, endpoint, err)
}

// OperatorDeployDirs returns the working directories for count Operators.
func OperatorDeployDirs(remoteDir string, count int) []string {
	if count == 1 {
		return []string{remoteDir}
	}
	dirs := make([]string, count)
	for i := range dirs {
		dirs[i] = fmt.Sprintf("%s/%s-%05d", strings.TrimSuffix(remoteDir, "/"), DirPrefix, i+1)
	}
	return dirs
}

// OperatorDeployArgsForDir expands {host}, {dir}, and {name} and assigns
// deterministic capability IDs when omitted.
func OperatorDeployArgsForDir(args []string, host, dir string) []string {
	replace := strings.NewReplacer("{host}", host, "{dir}", dir, "{name}", filepath.Base(dir))
	result := make([]string, 0, len(args)+2)
	flags := make(map[string]string)
	for _, arg := range args {
		arg = replace.Replace(arg)
		result = append(result, arg)
		key, value, _ := strings.Cut(arg, "=")
		flags[key] = value
	}
	digest := sha256.Sum256([]byte(host + "\x00" + dir))
	var capabilities constants.OperatorCapabilities
	if value := flags["--capabilities"]; value != "" {
		_ = capabilities.Set(value)
	}
	for capability, id := range map[constants.OperatorCapability]string{
		constants.OperatorCapabilityProvenance: "--provenance-operator-id",
		constants.OperatorCapabilityObserver:   "--provider-boundary-observer-id",
	} {
		if capabilities.Has(capability) && flags[id] == "" {
			result = append(result, fmt.Sprintf("%s=operator-%x", id, digest[:16]))
		}
	}
	return result
}

// DeployOperator installs the binary into dir on one host and, with Background,
// starts the worker there and awaits staging if an event watcher is set.
func DeployOperator(ctx context.Context, out io.Writer, s HostTransport, sourceBinary, dir string, opts StageOptions) (DeployedOperator, error) {
	if out == nil {
		out = io.Discard
	}
	absDir, err := s.PrepareDir(ctx, dir)
	if err != nil {
		return DeployedOperator{}, err
	}
	if !(s.Local && s.LocalStartFunc != nil && sourceBinary == "" && s.BinaryDir == "") {
		if err := s.InstallBinary(ctx, sourceBinary, absDir); err != nil {
			return DeployedOperator{}, err
		}
	}
	startArgs := OperatorDeployArgsForDir(opts.StartArgs, s.Host, absDir)
	if opts.LaunchIDFunc != nil {
		s.LaunchID, err = opts.LaunchIDFunc()
	} else {
		s.LaunchID, err = uuid.NewString()
	}
	if err != nil {
		return DeployedOperator{}, fmt.Errorf("deployment launch identity: %w", err)
	}
	startArgs = append(startArgs, "--deployment-id="+s.LaunchID)
	op := DeployedOperator{Target: s, Dir: absDir, LaunchID: s.LaunchID}
	if !opts.Background {
		_, _ = fmt.Fprintf(out, "Operator deployed to %s:%s (use --background to auto-start)\n", s.Host, absDir)
		return op, nil
	}
	if opts.Events != nil {
		// Watch before starting: the stream delivers only live events.
		op.Watch = opts.Events.Watch(s.LaunchID)
	}
	if err := s.StartOperator(ctx, absDir, opts.Endpoint, startArgs...); err != nil {
		return DeployedOperator{}, err
	}
	if op.Watch != nil {
		op.RequestID, err = AwaitStaged(ctx, op.Watch, s, absDir, opts.EnrollTimeout)
		if err != nil {
			return DeployedOperator{}, err
		}
	}
	if opts.ReadFingerprints {
		if op.Fingerprints, err = WorkerFingerprints(ctx, s, absDir); err != nil {
			return DeployedOperator{}, err
		}
	}
	_, _ = fmt.Fprintf(out, "Staged operator on %s (working dir %s, enrollment request %s)\n", s.Host, absDir, op.RequestID)
	return op, nil
}

// DeployOperatorBatch stages and starts dirs concurrently up to parallel workers.
func DeployOperatorBatch(ctx context.Context, host HostTransport, source string, dirs []string, opts StageOptions, parallel int) <-chan DeployOperatorResult {
	jobs := make(chan string)
	results := make(chan DeployOperatorResult)
	var workers sync.WaitGroup
	for i := 0; i < min(parallel, len(dirs)); i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for dir := range jobs {
				var output bytes.Buffer
				op, err := DeployOperator(ctx, &output, host, source, dir, opts)
				results <- DeployOperatorResult{Op: op, Dir: dir, Output: output.String(), Err: err}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, dir := range dirs {
			select {
			case jobs <- dir:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { workers.Wait(); close(results) }()
	return results
}

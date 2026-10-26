// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package serve

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/zerotrustdata/ztd/internal/certs"
	"github.com/zerotrustdata/ztd/internal/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/exitcode"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services"
	"github.com/zerotrustdata/ztd/internal/services/auth"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/gateway"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
	"github.com/zerotrustdata/ztd/internal/services/logging"
	"github.com/zerotrustdata/ztd/internal/services/pubsub"
)

// OperatorRuntimeDeps are the process-owned dependencies of one Operator
// runtime. ClientIdentity is the single in-memory holder of the Operator's
// mTLS identity; every outbound TLS connection reads it.
type OperatorRuntimeDeps struct {
	Logger         *slog.Logger
	ClientIdentity *certs.ClientIdentity
}

// OperatorRuntime is one started outbound Operator. It owns the service and
// its background work until Stop.
type OperatorRuntime struct {
	service     *services.ZtdoService
	logger      *slog.Logger
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	serviceErr  chan error
	runtimeLock io.Closer
}

// operatorStartError carries the process exit code for a startup failure.
type operatorStartError struct {
	code int
	err  error
}

func (e *operatorStartError) Error() string { return e.err.Error() }
func (e *operatorStartError) Unwrap() error { return e.err }

func startFailure(code int, err error) error {
	return &operatorStartError{code: code, err: err}
}

// operatorExitCode maps a StartOperator failure to the process exit code.
func operatorExitCode(err error) int {
	var startErr *operatorStartError
	if errors.As(err, &startErr) {
		return startErr.code
	}
	return exitcode.FromError(err)
}

// StartOperator resolves the Operator identity, constructs the service, and
// starts it. It returns once the service is starting; Failed reports a start
// failure and Done reports a service-requested shutdown.
func StartOperator(ctx context.Context, opts ServeOperatorOptions, vi VersionInfo, deps OperatorRuntimeDeps) (_ *OperatorRuntime, err error) {
	logger := deps.Logger
	if logger == nil || deps.ClientIdentity == nil {
		return nil, fmt.Errorf("%w: operator runtime requires a logger and client identity", constants.ErrInternal)
	}
	logger = logger.With(logging.ComponentKey, logging.ComponentOperator)
	clientIdentity := deps.ClientIdentity

	gatewayHost, httpPort, err := resolveOperatorGateway(opts.Endpoint, opts.HTTPPort)
	if err != nil {
		logger.Error("Invalid Gateway endpoint", string(constants.ConnectionStateError), err)
		return nil, startFailure(constants.ExitConfigError, err)
	}
	opts.HTTPPort = httpPort
	operatorEndpoint := config.GatewayDialHost(gatewayHost)
	if opts.Endpoint != "" {
		opts.Endpoint = operatorEndpoint
	}

	logger.Info("ztd", "version", vi.Version, "build", vi.BuildID)
	logger.Info("Using Operator endpoint", "endpoint", operatorEndpoint)

	// Resolve the worker root before constructing RuntimeFileService. The
	// --working-dir flag scopes both command execution and the worker's local
	// .ztd evidence stores; using the launch directory here silently split
	// those two concerns and made reports inspect the wrong vault and ledger.
	fileSvc, effectiveWorkDir, err := newOperatorRuntimeFileService(opts, logger)
	if err != nil {
		logger.Error("Failed to create file service", string(constants.ConnectionStateError), err)
		return nil, startFailure(exitcode.FromError(err), err)
	}
	if err := refuseGatewayRuntime(ctx, fileSvc, effectiveWorkDir); err != nil {
		logger.Error("Refusing to start Operator", string(constants.ConnectionStateError), err)
		return nil, startFailure(constants.ExitConfigError, err)
	}
	for _, modelRoot := range []string{opts.ProvenanceOperatorModelStorageRoot, opts.InferenceLlamaCppModelRoot} {
		if err := refuseModelStoreRuntime(fileSvc.Resolve(""), modelRoot); err != nil {
			logger.Error("Refusing to start Operator", string(constants.ConnectionStateError), err)
			return nil, startFailure(constants.ExitConfigError, err)
		}
	}
	// Own the runtime before touching its vault, database, or enrollment state.
	// Capability sets distinguish enrollments, never concurrent owners of a root.
	runtimeLock, err := fileSvc.TryLock(ctx, constants.OperatorLockFilename, constants.PermFilePrivate)
	if err != nil {
		return nil, startFailure(constants.ExitConfigError, fmt.Errorf("operator start: lock runtime %s; use a separate --working-dir for each Operator, or combine --capabilities in one process: %w", fileSvc.Resolve(""), err))
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, runtimeLock.Close())
		}
	}()

	if err := fileSvc.CreateRuntimeTree(ctx); err != nil {
		logger.Error("Failed to create runtime tree", string(constants.ConnectionStateError), err)
		return nil, startFailure(exitcode.FromError(err), err)
	}
	deployment, err := NewOperatorDeploymentRecorder(fileSvc, opts.DeploymentID)
	if err != nil {
		logger.Error("Failed to create deployment recorder", "error", err)
		return nil, startFailure(exitcode.FromError(err), err)
	}
	if err := deployment.Reset(ctx); err != nil {
		logger.Error("Failed to reset deployment state", string(constants.ConnectionStateError), err)
		return nil, startFailure(exitcode.FromError(err), err)
	}

	// Keep early configuration/enrollment failures observable to the deployer.
	recordDeploymentFailure := func() {
		if err := deployment.Record(context.Background(), models.OperatorDeploymentState{Phase: models.OperatorDeploymentPhaseFailed, Error: constants.ErrOperatorDeployFailed.Error()}); err != nil {
			logger.Error("Failed to record deployment failure", "error", err)
		}
	}
	defer func() {
		if err != nil {
			recordDeploymentFailure()
		}
	}()

	cfg, err := config.Load(buildOperatorLoadOptions(opts, operatorEndpoint, effectiveWorkDir))
	if err != nil {
		exitCode, actionable := classifyConfigLoadError(err)
		logger.Error("Failed to load configuration", string(constants.ConnectionStateError), err)
		if actionable != "" {
			fmt.Fprintln(os.Stderr, actionable)
		}
		return nil, startFailure(exitCode, err)
	}
	cfg.Version = vi.Version

	// The store is opened once, before enrollment. The Actuator key presented at
	// enrollment and the key that signs receipts both come from this open.
	ks, err := keystore.Open(fileSvc, logger, keystore.Options{Keyring: cfg.Keyring})
	if err != nil {
		logger.Error("Failed to open keystore", string(constants.ConnectionStateError), err)
		return nil, startFailure(exitcode.FromError(err), fmt.Errorf("%w: %w", constants.ErrGatewayDatabaseServiceNotConfigured, err))
	}
	canonicalDB, err := gateway.OpenCanonicalDBService(logger, ks, fileSvc)
	if err != nil {
		logger.Error("Failed to open canonical database", string(constants.ConnectionStateError), err)
		return nil, startFailure(exitcode.FromError(err), fmt.Errorf("%w: %w", constants.ErrGatewayDatabaseServiceNotConfigured, err))
	}
	// Until the service takes ownership, this function closes the database on
	// every error return. Once NewZtdoService succeeds, ZtdoService.Stop closes it.
	dbOwnedByService := false
	defer func() {
		if !dbOwnedByService {
			if closeErr := canonicalDB.Close(); closeErr != nil {
				logger.Error("Failed to close canonical database", "error", closeErr)
			}
		}
	}()

	actuatorPriv, _, err := canonicalDB.GetSecretManager().GetActuatorKey()
	if err != nil {
		logger.Error("Failed to read Actuator key", string(constants.ConnectionStateError), err)
		return nil, startFailure(exitcode.FromError(err), fmt.Errorf("%w: %w", constants.ErrKeyReadFailed, err))
	}
	actuatorPublicKeyHex := hex.EncodeToString(actuatorPriv.Public().(ed25519.PublicKey))
	auditorPriv, _, err := canonicalDB.GetSecretManager().GetAuditorKey()
	if err != nil {
		logger.Error("Failed to read Auditor key", string(constants.ConnectionStateError), err)
		return nil, startFailure(exitcode.FromError(err), fmt.Errorf("%w: %w", constants.ErrKeyReadFailed, err))
	}
	auditorPublicKeyHex := hex.EncodeToString(auditorPriv.Public().(ed25519.PublicKey))

	// An owner-supplied CA is read, never persisted, and wins over the
	// trust delivered in the bundle.
	var explicitTrust []byte
	if opts.TrustBundlePath != "" {
		explicitTrust, err = os.ReadFile(opts.TrustBundlePath)
		if err != nil || !x509.NewCertPool().AppendCertsFromPEM(explicitTrust) {
			logger.Error("Invalid explicit trust bundle", "path", opts.TrustBundlePath)
			return nil, startFailure(constants.ExitConfigError, fmt.Errorf("%w: invalid explicit trust bundle %s", constants.ErrCAParseFailed, opts.TrustBundlePath))
		}
	}

	identity, err := enrollOperator(ctx, opts, cfg, operatorEndpoint, effectiveWorkDir, explicitTrust, actuatorPublicKeyHex, auditorPublicKeyHex, deployment, logger)
	if err != nil {
		logger.Error("Operator enrollment failed", string(constants.ConnectionStateError), err)
		fmt.Fprintf(os.Stderr, "Enrollment failed: %v\n", err)
		fmt.Fprintf(os.Stderr, "  Ensure the Gateway is running and accessible at %s\n", operatorEndpoint)
		return nil, startFailure(constants.ExitConfigError, err)
	}

	trustStore := certs.NewTrustStore(nil)
	if explicitTrust != nil {
		trustStore.SetCA(explicitTrust)
	} else {
		trustStore.SetCA(identity.TrustBundlePEM)
	}
	clientIdentity.SetCertificate(identity.Certificate)
	LogCertBundle(logger, "client-cert", identity.CertificatePEM)
	tlsConfig := certs.NewTLSConfig(trustStore, clientIdentity)

	l2Trust, err := governance.NewL2TrustStore(identity.L2Trust)
	if err != nil {
		logger.Error("Invalid L2 trust in enrollment bundle", "error", err)
		return nil, startFailure(constants.ExitConfigError, err)
	}

	cfg.OperatorID = identity.OperatorID
	cfg.OperatorSessionId = identity.OperatorSessionID
	cfg.SystemFingerprint = identity.SystemFingerprint
	cfg.Posture = identity.Posture
	if identity.MaxConcurrentTasks > 0 {
		cfg.MaxConcurrentTasks = identity.MaxConcurrentTasks
	}
	if identity.MaxMemoryMB > 0 {
		cfg.MaxMemoryMB = identity.MaxMemoryMB
	}

	if cfg.CloudMode {
		logger.Info("Cloud Operator mode enabled", "provider", cfg.CloudProvider)
	}

	ztdoService, err := services.NewZtdoService(cfg, logger, tlsConfig, l2Trust, fileSvc, canonicalDB, func(baseURL, serverName string, logger *slog.Logger, tlsConfig *certs.TLSConfig) (pubsub.PubSubClient, error) {
		client, err := pubsub.NewOperatorPubSubClient(baseURL, serverName, logger, tlsConfig)
		if err != nil {
			return nil, err
		}
		client.SetDeploymentID(opts.DeploymentID)
		return client, nil
	})
	if err != nil {
		logger.Error("Failed to create Operator service", string(constants.ConnectionStateError), err)
		return nil, startFailure(exitcode.FromError(err), err)
	}
	dbOwnedByService = true

	if err := ztdoService.SetCommandSubscriptionObserver(deployment.CommandSubscriptionChanged); err != nil {
		logger.Error("Failed to configure deployment observer", "error", err)
		return nil, startFailure(exitcode.FromError(err), err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	rt := &OperatorRuntime{
		service:     ztdoService,
		logger:      logger,
		cancel:      cancel,
		serviceErr:  make(chan error, 1),
		runtimeLock: runtimeLock,
	}

	rt.wg.Add(1)
	go func() {
		defer rt.wg.Done()
		if err := ztdoService.Start(runCtx); err != nil {
			recordDeploymentFailure()
			logger.Error("Failed to start ztd", string(constants.ConnectionStateError), err)
			rt.serviceErr <- err
		}
	}()

	renewer := NewOperatorCertRenewer(func() (pubsub.PubSubClient, error) {
		return pubsub.NewOperatorPubSubClient(cfg.PubSubURL, cfg.TLSServerName, logger, tlsConfig)
	}, clientIdentity, l2Trust, identity.OperatorID, identity.OperatorSessionID, logger)
	rt.wg.Add(1)
	go func() {
		defer rt.wg.Done()
		renewer.Run(runCtx)
	}()

	return rt, nil
}

// enrollOperator obtains the Operator's in-memory identity over the Gateway
// bootstrap websocket. Every start enrolls: no identity survives the process.
// actuatorPublicKeyHex is the hex Ed25519 key of this Operator's Actuator, which
// the Gateway binds to the Operator at enrollment.
func enrollOperator(ctx context.Context, opts ServeOperatorOptions, cfg *config.Config, endpoint, workDir string, explicitTrust []byte, actuatorPublicKeyHex, auditorPublicKeyHex string, deployment *OperatorDeploymentRecorder, logger *slog.Logger) (*OperatorIdentity, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("resolve hostname: %w", err)
	}
	runtimeConfig, err := operatorRuntimeConfig(cfg)
	if err != nil {
		return nil, err
	}
	client, err := NewOperatorBootstrapClient(OperatorBootstrapClientConfig{
		URL:                operatorBootstrapDialURL(cfg, endpoint, opts.HTTPPort, explicitTrust != nil),
		InstanceID:         operatorInstanceID(hostname, operatorCapabilities(opts), workDir),
		Hostname:           hostname,
		RuntimeConfig:      runtimeConfig,
		FingerprintOptions: operatorFingerprintOptions(opts, workDir, auth.ResolveCurrentAccount()),
		ActuatorPublicKey:  actuatorPublicKeyHex,
		AuditorPublicKey:   auditorPublicKeyHex,
		TrustBundlePEM:     explicitTrust,
		TLSServerName:      cfg.TLSServerName,
		Deployment:         deployment,
		Logger:             logger,
	})
	if err != nil {
		return nil, err
	}
	logger.Info("Enrolling Operator over the bootstrap websocket", "endpoint", endpoint, "pinned_trust", explicitTrust != nil)
	return client.Enroll(ctx)
}

// Failed delivers the service start failure, if any.
func (rt *OperatorRuntime) Failed() <-chan error {
	return rt.serviceErr
}

// Done is closed when the Operator service requests shutdown.
func (rt *OperatorRuntime) Done() <-chan struct{} {
	return rt.service.Done()
}

// Stop cancels the runtime, joins its background work, and stops the service.
func (rt *OperatorRuntime) Stop(ctx context.Context) error {
	rt.cancel()
	rt.wg.Wait()
	return errors.Join(rt.service.Stop(ctx), rt.runtimeLock.Close())
}

// refuseGatewayRuntime keeps an outbound Operator out of a Gateway's runtime
// tree. Each executing runtime owns its own local-first events store and state root, so an
// Operator launched from the Gateway directory without --working-dir would
// otherwise open the Gateway's databases and vault.
func refuseGatewayRuntime(ctx context.Context, fileSvc fs.RuntimeFileService, workDir string) error {
	isGateway, err := fileSvc.FileExists(ctx, constants.GatewayRuntimeMarker)
	if err != nil {
		return fmt.Errorf("operator start: inspect runtime %s: %w", workDir, err)
	}
	if isGateway {
		return fmt.Errorf("%w: %s; pass --working-dir with a separate directory, for example ~/.ztd-operator", constants.ErrOperatorGatewayRuntime, fileSvc.Resolve(""))
	}
	return nil
}

// refuseModelStoreRuntime keeps an Operator's runtime tree (.ztd) out of a
// model storage root. The model store is a read-only witness and weight cache;
// an Operator launched with its working directory inside the model store would
// place runtime state, logs, and databases inside the model repository.
func refuseModelStoreRuntime(runtimeDir, modelStorageRoot string) error {
	modelRoot := strings.TrimSpace(modelStorageRoot)
	if modelRoot == "" {
		return nil
	}
	rt := strings.TrimSpace(runtimeDir)
	if rt == "" {
		return nil
	}

	absModelRoot, err := filepath.Abs(modelRoot)
	if err != nil {
		absModelRoot = filepath.Clean(modelRoot)
	}
	absRuntime, err := filepath.Abs(rt)
	if err != nil {
		absRuntime = filepath.Clean(rt)
	}

	checkModelRoot := absModelRoot
	checkRuntime := absRuntime
	if runtime.GOOS == "windows" {
		checkModelRoot = strings.ToLower(checkModelRoot)
		checkRuntime = strings.ToLower(checkRuntime)
	}

	rel, err := filepath.Rel(checkModelRoot, checkRuntime)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: %s; pass --working-dir with a separate directory outside the model store", constants.ErrOperatorModelStoreRuntime, absRuntime)
	}
	return nil
}

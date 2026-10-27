// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
)

// PublicSpectatorConfig configures the gateway-owned public mirror, publisher
// retransmit loop, and evaluation explorer static host.
type PublicSpectatorConfig struct {
	Enabled               bool
	PrivateListenAddress  string
	PublicListenAddress   string
	ExplorerListenAddress string
	PublicBaseURL         string
	SourceID              string
	ExplorerRoot          string
	TrustedProxyCIDRs     []string
	// AllowContainerBind permits the mirror and explorer listeners to bind
	// 0.0.0.0 so the container network can reach them. It is set only by the
	// explicit --public-spectator-allow-container-bind gateway flag.
	AllowContainerBind bool
}

// DefaultPublicSpectatorConfig returns loopback defaults for local development.
func DefaultPublicSpectatorConfig() PublicSpectatorConfig {
	return PublicSpectatorConfig{
		Enabled:               true,
		PrivateListenAddress:  fmt.Sprintf("127.0.0.1:%d", constants.PublicSpectatorPrivatePort),
		PublicListenAddress:   fmt.Sprintf("127.0.0.1:%d", constants.PublicSpectatorPublicPort),
		ExplorerListenAddress: fmt.Sprintf("127.0.0.1:%d", constants.EvalExplorerDefaultPort),
		SourceID:              defaultPublicSpectatorSourceID,
	}
}

// PublicSpectatorRuntime owns the in-process public mirror listeners and the
// optional evaluation explorer static host.
type PublicSpectatorRuntime struct {
	cfg       PublicSpectatorConfig
	fileSvc   fs.RuntimeFileService
	keystore  *keystore.Keystore
	logger    *slog.Logger
	publisher *PublicPublisherService
	mirror    *PublicMirrorServer
	store     *PublicMirrorStore

	privateServer  *http.Server
	publicServer   *http.Server
	explorerServer *http.Server

	mu      sync.Mutex
	running bool
}

// Mirror returns the gateway-owned public mirror server when the spectator
// runtime has started.
func (runtime *PublicSpectatorRuntime) Mirror() *PublicMirrorServer {
	if runtime == nil {
		return nil
	}
	return runtime.mirror
}

// NewPublicSpectatorRuntime prepares the gateway-owned public spectator stack.
func NewPublicSpectatorRuntime(cfg PublicSpectatorConfig, fileSvc fs.RuntimeFileService, ks *keystore.Keystore, logger *slog.Logger) (*PublicSpectatorRuntime, error) {
	if ks == nil {
		return nil, fmt.Errorf("public spectator: %w: keystore", constants.ErrMissingRequiredField)
	}
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.PrivateListenAddress == "" {
		cfg.PrivateListenAddress = DefaultPublicSpectatorConfig().PrivateListenAddress
	}
	if cfg.PublicListenAddress == "" {
		cfg.PublicListenAddress = DefaultPublicSpectatorConfig().PublicListenAddress
	}
	if err := ValidatePublicMirrorListenAddresses(cfg.PrivateListenAddress, cfg.PublicListenAddress, cfg.AllowContainerBind); err != nil {
		return nil, err
	}
	if err := ValidatePublicExplorerListenAddress(cfg.ExplorerListenAddress, cfg.PrivateListenAddress, cfg.PublicListenAddress, cfg.AllowContainerBind); err != nil {
		return nil, err
	}
	return &PublicSpectatorRuntime{
		cfg:      cfg,
		fileSvc:  fileSvc,
		keystore: ks,
		logger:   logger,
	}, nil
}

// Start launches mirror listeners, wires the publisher retransmit loop, and
// serves the evaluation explorer when static assets are available.
func (runtime *PublicSpectatorRuntime) Start(ctx context.Context) error {
	if runtime == nil {
		return fmt.Errorf("public spectator: %w", constants.ErrMissingRequiredField)
	}
	runtime.mu.Lock()
	if runtime.running {
		runtime.mu.Unlock()
		return nil
	}
	runtime.mu.Unlock()

	// Bind the mirror listeners first so port 0 is assigned atomically by the
	// kernel and held before feed and explorer origins are materialized.
	dedicatedExplorer := shouldServeDedicatedExplorer(runtime.cfg)
	privateListener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", runtime.cfg.PrivateListenAddress)
	if err != nil {
		return fmt.Errorf("public spectator mirror-private: listen %s: %w", runtime.cfg.PrivateListenAddress, err)
	}
	started := false
	defer func() {
		if !started {
			_ = privateListener.Close()
		}
	}()
	runtime.cfg.PrivateListenAddress = privateListener.Addr().String()

	publicListener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", runtime.cfg.PublicListenAddress)
	if err != nil {
		return fmt.Errorf("public spectator mirror-public: listen %s: %w", runtime.cfg.PublicListenAddress, err)
	}
	defer func() {
		if !started {
			_ = publicListener.Close()
		}
	}()
	runtime.cfg.PublicListenAddress = publicListener.Addr().String()
	if !dedicatedExplorer && strings.TrimSpace(runtime.cfg.ExplorerListenAddress) != "" {
		runtime.cfg.ExplorerListenAddress = runtime.cfg.PublicListenAddress
	}

	privateMirrorOrigin := "http://" + runtime.cfg.PrivateListenAddress
	exportConfig, err := ReadPublicExportConfig(ctx, runtime.fileSvc)
	if errors.Is(err, constants.ErrPublicFeedConfigRequired) && errors.Is(err, constants.ErrNotFound) {
		sourceID := strings.TrimSpace(runtime.cfg.SourceID)
		if sourceID == "" {
			sourceID = defaultPublicSpectatorSourceID
		}
		origin := strings.TrimSpace(privateMirrorOrigin)
		if origin == "" {
			origin = fmt.Sprintf("http://127.0.0.1:%d", constants.PublicSpectatorPrivatePort)
		}
		exportConfig, err = InitializeLocalPublicFeed(ctx, runtime.fileSvc, runtime.keystore, sourceID, origin)
	}
	if err != nil {
		return fmt.Errorf("public spectator: load or initialize feed: %w", err)
	}
	if exportConfig.MirrorOrigin == "" {
		exportConfig.MirrorOrigin = "http://" + runtime.cfg.PrivateListenAddress
	} else if _, port, splitErr := net.SplitHostPort(runtime.cfg.PrivateListenAddress); splitErr == nil {
		expectedPrivate := "http://" + runtime.cfg.PrivateListenAddress
		expectedLocalhost := fmt.Sprintf("http://localhost:%s", port)
		if exportConfig.MirrorOrigin != expectedPrivate && exportConfig.MirrorOrigin != expectedLocalhost {
			runtime.logger.Warn("Public feed mirror origin does not match gateway private listener",
				"mirror_origin", exportConfig.MirrorOrigin,
				"private_listen", runtime.cfg.PrivateListenAddress)
		}
	}

	key, err := ReadPublicSecret(ctx, runtime.fileSvc, runtime.keystore, constants.PublicFeedSigningKeyPath, ed25519.PrivateKeySize, constants.ErrPublicFeedSigningKeyRequired)
	if err != nil {
		return fmt.Errorf("public spectator: %w", err)
	}
	token, err := ReadPublicSecret(ctx, runtime.fileSvc, runtime.keystore, constants.PublicFeedIngestTokenPath, constants.PublicFeedIngestTokenBytes, constants.ErrPublicFeedIngestTokenRequired)
	if err != nil {
		return fmt.Errorf("public spectator: %w", err)
	}

	store, err := OpenPublicMirrorStore(ctx, runtime.fileSvc, runtime.logger)
	if err != nil {
		return fmt.Errorf("public spectator: %w", err)
	}
	defer func() {
		if !started {
			_ = store.Close()
		}
	}()
	mirror, err := NewPublicMirrorServer(runtime.logger, store, PublicMirrorConfig{
		TrustedProxyCIDRs: runtime.cfg.TrustedProxyCIDRs,
	})
	if err != nil {
		return err
	}
	mirror.SetIngestAuthToken(hex.EncodeToString(token))
	privateKey := ed25519.PrivateKey(key)
	if err := mirror.RegisterSourceKey(ctx, exportConfig.SourceID, exportConfig.SigningKeyID, privateKey.Public().(ed25519.PublicKey)); err != nil {
		return fmt.Errorf("public spectator: register source key: %w", err)
	}

	publisher := NewPublicPublisherService(nil, runtime.fileSvc, runtime.logger, exportConfig, privateKey, exportConfig.SigningKeyID)
	publisher.SetIngestAuthToken(hex.EncodeToString(token))
	publisher.SetMirrorOrigin("http://" + runtime.cfg.PrivateListenAddress)
	// The publisher's own signed batches land in this store, so assignment
	// audit databases are built from the rows it holds.
	publisher.SetAssignmentEventSource(store)

	runtime.privateServer = newPublicMirrorHTTPServer(runtime.cfg.PrivateListenAddress, mirror.IngestHandler(), runtime.logger.With("server", "mirror-private"))
	mirrorOrigin := resolveEvalExplorerMirrorOrigin(runtime.cfg.PublicBaseURL, runtime.cfg.PublicListenAddress)
	publicHandler := mirror.PublicHandler()
	explorerHandler, explorerErr := NewEvalExplorerHandler(runtime.cfg.ExplorerRoot, mirrorOrigin, false)
	if explorerErr == nil {
		publicHandler = combinePublicSpectatorHandler(publicHandler, explorerHandler)
	} else {
		runtime.logger.Warn("Evaluation explorer static assets unavailable", "error", explorerErr)
	}
	runtime.publicServer = newPublicMirrorHTTPServer(runtime.cfg.PublicListenAddress, publicHandler, runtime.logger.With("server", "mirror-public"))
	var explorerListener net.Listener
	if explorerErr == nil && dedicatedExplorer {
		explorerListener, err = (&net.ListenConfig{}).Listen(ctx, "tcp", runtime.cfg.ExplorerListenAddress)
		if err != nil {
			return fmt.Errorf("public spectator explorer: listen %s: %w", runtime.cfg.ExplorerListenAddress, err)
		}
		defer func() {
			if !started {
				_ = explorerListener.Close()
			}
		}()
		runtime.cfg.ExplorerListenAddress = explorerListener.Addr().String()
		// Dedicated explorer listeners (for example :5173) must target the local
		// public mirror API (:8082), not the gateway HTTPS base URL used for
		// approval links and tunnel fronting.
		dedicatedMirrorOrigin := publicMirrorURL(runtime.cfg.PublicListenAddress)
		dedicatedHandler, dedicatedErr := NewEvalExplorerHandler(runtime.cfg.ExplorerRoot, dedicatedMirrorOrigin, true)
		if dedicatedErr != nil {
			return fmt.Errorf("public spectator: dedicated explorer: %w", dedicatedErr)
		}
		runtime.explorerServer = newPublicMirrorHTTPServer(runtime.cfg.ExplorerListenAddress, dedicatedHandler, runtime.logger.With("server", "explorer"))
	}
	runtime.mirror = mirror
	runtime.publisher = publisher

	// Every listener is bound before Start returns: a bind failure fails Start,
	// and once Start returns, connections queue on the socket until served.
	type spectatorListener struct {
		server   *http.Server
		label    string
		listener net.Listener
	}
	bound := []spectatorListener{
		{server: runtime.privateServer, label: "mirror-private", listener: privateListener},
		{server: runtime.publicServer, label: "mirror-public", listener: publicListener},
	}
	if runtime.explorerServer != nil {
		bound = append(bound, spectatorListener{server: runtime.explorerServer, label: "explorer", listener: explorerListener})
	}

	errCh := make(chan error, len(bound))
	for _, surface := range bound {
		go func() {
			runtime.logger.Info("Public spectator listener started", "surface", surface.label, "addr", surface.server.Addr)
			if serveErr := surface.server.Serve(surface.listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
				errCh <- fmt.Errorf("public spectator %s: %w", surface.label, serveErr)
			}
		}()
	}

	started = true
	runtime.mu.Lock()
	runtime.store = store
	runtime.running = true
	runtime.mu.Unlock()

	go runtime.runPublisherRetransmitLoop(ctx)

	go func() {
		select {
		case serveErr := <-errCh:
			if serveErr != nil {
				runtime.logger.Error("Public spectator listener failed", "error", serveErr)
			}
		case <-ctx.Done():
		}
	}()

	return nil
}

// Serve starts the spectator listeners and blocks until ctx is cancelled.
func (runtime *PublicSpectatorRuntime) Serve(ctx context.Context) error {
	if err := runtime.Start(ctx); err != nil {
		return err
	}
	<-ctx.Done()
	stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return runtime.Stop(stopCtx)
}

// Stop shuts down mirror and explorer listeners.
func (runtime *PublicSpectatorRuntime) Stop(ctx context.Context) error {
	if runtime == nil {
		return nil
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if !runtime.running {
		return nil
	}
	runtime.running = false
	store := runtime.store
	runtime.store = nil

	shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// Ending open SSE streams lets Shutdown drain instead of waiting them out.
	if runtime.mirror != nil {
		runtime.mirror.CloseAllSubscribers()
	}
	var errs []error
	for _, server := range []*http.Server{runtime.privateServer, runtime.publicServer, runtime.explorerServer} {
		if server == nil {
			continue
		}
		if err := server.Shutdown(shutdownCtx); err != nil {
			errs = append(errs, fmt.Errorf("public spectator: shutdown %s: %w", server.Addr, err))
		}
	}
	if store != nil {
		if err := store.Close(); err != nil {
			errs = append(errs, fmt.Errorf("public spectator: close mirror store: %w", err))
		}
	}
	return errors.Join(errs...)
}

// Running reports whether the spectator listeners were started.
func (runtime *PublicSpectatorRuntime) Running() bool {
	if runtime == nil {
		return false
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.running
}

// Publisher returns the in-process publisher used for outbox retransmit.
func (runtime *PublicSpectatorRuntime) Publisher() *PublicPublisherService {
	if runtime == nil {
		return nil
	}
	return runtime.publisher
}

// PublicListenAddress returns the anonymous mirror listen address.
func (runtime *PublicSpectatorRuntime) PublicListenAddress() string {
	if runtime == nil {
		return ""
	}
	return runtime.cfg.PublicListenAddress
}

func shouldServeDedicatedExplorer(cfg PublicSpectatorConfig) bool {
	if strings.TrimSpace(cfg.ExplorerListenAddress) == "" {
		return false
	}
	return !sameListenAddress(cfg.ExplorerListenAddress, cfg.PublicListenAddress)
}

func (runtime *PublicSpectatorRuntime) runPublisherRetransmitLoop(ctx context.Context) {
	if runtime.publisher == nil {
		return
	}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := runtime.publisher.RetransmitOutbox(ctx); err != nil {
				runtime.logger.Debug("Public publisher retransmit", "error", err)
			}
		}
	}
}

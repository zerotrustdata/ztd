// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build linux

package keystore

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"sync"

	"golang.org/x/crypto/ssh/agent"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

// EphemeralAgent is a private ssh-agent served from this process. It holds one
// Ed25519 key that exists only in memory, so every runtime a multi-process
// harness (release smoke, scale) seals under it is unreadable once the agent
// stops, and the host's Secret Service and ssh-agent are never touched.
type EphemeralAgent struct {
	sock     string
	noBus    string
	listener *net.UnixListener
	wg       sync.WaitGroup
}

// StartEphemeralAgent serves a fresh agent on a socket in relDir under
// fileSvc. relDir is created owner-only, and connections from other users are
// refused, because whoever can sign with the agent key can unseal the
// harness's master keys.
func StartEphemeralAgent(ctx context.Context, fileSvc fs.RuntimeFileService, relDir string) (*EphemeralAgent, error) {
	if err := fileSvc.MkdirAll(ctx, relDir, constants.PermDirPrivate); err != nil {
		return nil, fmt.Errorf("ephemeral ssh-agent: create socket dir: %w", err)
	}
	dir := fileSvc.Resolve(relDir)
	sock := filepath.Join(dir, constants.EphemeralAgentSocketFilename)
	if len(sock) > constants.UnixSocketPathMax {
		return nil, fmt.Errorf("%w: ephemeral socket path %s is longer than %d bytes", constants.ErrKeyStoreSSHAgentUnavailable, sock, constants.UnixSocketPathMax)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("ephemeral ssh-agent: generate key: %w", err)
	}
	keyring := agent.NewKeyring()
	if err := keyring.Add(agent.AddedKey{PrivateKey: priv, Comment: "ztd ephemeral harness key"}); err != nil {
		return nil, fmt.Errorf("ephemeral ssh-agent: add key: %w", err)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("%w: listen %s: %w", constants.ErrKeyStoreSSHAgentUnavailable, sock, err)
	}
	a := &EphemeralAgent{sock: sock, noBus: filepath.Join(dir, constants.EphemeralAgentNoBusFilename), listener: listener}
	a.wg.Add(1)
	go a.serve(keyring)
	return a, nil
}

func (a *EphemeralAgent) serve(keyring agent.Agent) {
	defer a.wg.Done()
	for {
		conn, err := a.listener.AcceptUnix()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		if err := checkSSHAgentPeer(conn); err != nil {
			_ = conn.Close()
			continue
		}
		a.wg.Add(1)
		go func() {
			defer a.wg.Done()
			defer conn.Close()
			_ = agent.ServeAgent(keyring, conn)
		}()
	}
}

// Env is the environment a harness child needs to seal under this agent:
// SSH_AUTH_SOCK names it, and the D-Bus session address points at a socket
// that never exists so the Secret Service is down for the child.
func (a *EphemeralAgent) Env() []string {
	return []string{
		string(constants.EnvVar.SSHAuthSock) + "=" + a.sock,
		string(constants.EnvVar.DBusSessionBusAddress) + "=unix:path=" + a.noBus,
	}
}

// Keyring returns the agent-sealed keyring for the runtime behind fileSvc, for
// harness code that reads a child runtime's store in-process.
func (a *EphemeralAgent) Keyring(fileSvc fs.RuntimeFileService) Keyring {
	return newSSHAgentKeyring(fileSvc, func() (agent.ExtendedAgent, func() error, error) {
		return dialSSHAgentSocket(a.sock)
	})
}

// Close stops the agent and waits for in-flight requests; the key is gone.
func (a *EphemeralAgent) Close() error {
	err := a.listener.Close()
	a.wg.Wait()
	if err != nil && !errors.Is(err, net.ErrClosed) {
		return fmt.Errorf("ephemeral ssh-agent: close: %w", err)
	}
	return nil
}

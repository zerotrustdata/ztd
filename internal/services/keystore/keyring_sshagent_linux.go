// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build linux

package keystore

import (
	"bytes"
	"context"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/sys/unix"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/vault"
)

const (
	sshAgentSealVersion = 1
	sshAgentChallenge   = "ztd-master-key-seal-v1:"
	sshAgentSaltSize    = 32
)

// sshAgentSeal is the on-disk form of the ssh-agent-sealed master key.
type sshAgentSeal struct {
	Version    int    `json:"version"`
	PublicKey  []byte `json:"public_key"`
	Salt       []byte `json:"salt"`
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
}

// sshAgentDialer opens an agent connection for one keyring operation.
type sshAgentDialer func() (agent.ExtendedAgent, func() error, error)

// SSHAgentUnlockError identifies the original sealing key without exposing
// private material. Callers can offer recovery without parsing error text.
type SSHAgentUnlockError struct {
	Fingerprint string
	Err         error
}

func (e *SSHAgentUnlockError) Error() string {
	return fmt.Sprintf("%s (required SSH key: %s)", e.Err, e.Fingerprint)
}

func (e *SSHAgentUnlockError) Unwrap() error { return e.Err }

// CheckSSHAgentMasterKey verifies an existing Linux agent seal before a CLI
// detaches from its terminal. A fresh runtime needs no check. This never creates
// or replaces a master key, switches providers, or retains the unsealed key.
func CheckSSHAgentMasterKey(ctx context.Context, fileSvc fs.RuntimeFileService) error {
	return checkSSHAgentMasterKey(ctx, fileSvc, dialSSHAgent)
}

func checkSSHAgentMasterKey(ctx context.Context, fileSvc fs.RuntimeFileService, dial sshAgentDialer) error {
	if fileSvc == nil {
		return fmt.Errorf("check ssh-agent master key: %w: runtime file service", constants.ErrMissingRequiredField)
	}
	key, err := newSSHAgentKeyring(fileSvc, dial).retrieveMasterKey(ctx)
	vault.SecureZero(key)
	if errors.Is(err, constants.ErrKeyStoreKeyNotFound) {
		return nil
	}
	return err
}

// sshAgentKeyring seals the master key under a key-encryption key derived from
// an ssh-agent signature over a random salt. Ed25519 and RSA (rsa-sha2-512)
// signatures are deterministic, so only the holder of that SSH private key can
// re-derive the KEK. Like DPAPI, the sealed blob lives in the secrets
// directory and is useless without the user's key.
type sshAgentKeyring struct {
	fileSvc fs.RuntimeFileService
	path    string
	dial    sshAgentDialer
}

func newSSHAgentKeyring(fileSvc fs.RuntimeFileService, dial sshAgentDialer) *sshAgentKeyring {
	return &sshAgentKeyring{
		fileSvc: fileSvc,
		path:    filepath.Join(constants.SecretsDirname, constants.MasterKeySSHAgentFilename),
		dial:    dial,
	}
}

// dialSSHAgent connects to the agent named by SSH_AUTH_SOCK. When it is unset,
// as in SSH logins without -A, it dials the user's systemd agent sockets under
// XDG_RUNTIME_DIR in order. Either way the process serving the socket must run
// as this user or root.
func dialSSHAgent() (agent.ExtendedAgent, func() error, error) {
	if sock := os.Getenv(string(constants.EnvVar.SSHAuthSock)); sock != "" {
		return dialSSHAgentSocket(sock)
	}
	runtimeDir := os.Getenv(string(constants.EnvVar.XDGRuntimeDir))
	if runtimeDir == "" {
		runtimeDir = constants.UserRuntimeDirPrefix + strconv.Itoa(os.Geteuid())
	}
	var tried []string
	for _, rel := range []string{constants.GcrSSHAgentSocketRelPath, constants.OpenSSHAgentSocketRelPath} {
		sock := filepath.Join(runtimeDir, rel)
		ag, closeAgent, err := dialSSHAgentSocket(sock)
		if err == nil {
			return ag, closeAgent, nil
		}
		if errors.Is(err, constants.ErrKeyStoreSSHAgentUntrusted) {
			return nil, nil, fmt.Errorf("%s: %w", sock, err)
		}
		tried = append(tried, sock)
	}
	return nil, nil, fmt.Errorf("%w: SSH_AUTH_SOCK is not set and no user agent answers at %s",
		constants.ErrKeyStoreSSHAgentUnavailable, strings.Join(tried, ", "))
}

func dialSSHAgentSocket(sock string) (agent.ExtendedAgent, func() error, error) {
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", constants.ErrKeyStoreSSHAgentUnavailable, err)
	}
	if err := checkSSHAgentPeer(conn); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	return agent.NewClient(conn), conn.Close, nil
}

// checkSSHAgentPeer rejects an agent served by another user. Whoever serves
// the socket chooses the sealing key and sees every signature, so a foreign
// agent would learn the KEK.
func checkSSHAgentPeer(conn *net.UnixConn) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return fmt.Errorf("%w: %w", constants.ErrKeyStoreSSHAgentUnavailable, err)
	}
	var cred *unix.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return fmt.Errorf("%w: %w", constants.ErrKeyStoreSSHAgentUnavailable, err)
	}
	if credErr != nil {
		return fmt.Errorf("%w: peer credentials: %w", constants.ErrKeyStoreSSHAgentUnavailable, credErr)
	}
	if cred.Uid != uint32(os.Geteuid()) && cred.Uid != 0 {
		return fmt.Errorf("%w: uid %d", constants.ErrKeyStoreSSHAgentUntrusted, cred.Uid)
	}
	return nil
}

func (s *sshAgentKeyring) Name() string {
	return "ssh-agent"
}

// sealed reports whether a sealed master key exists.
func (s *sshAgentKeyring) sealed() (bool, error) {
	exists, err := s.fileSvc.FileExists(context.Background(), s.path)
	if err != nil {
		return false, fmt.Errorf("ssh-agent: stat sealed master key: %w", err)
	}
	return exists, nil
}

// usable reports whether the agent answers and holds a key that can seal.
func (s *sshAgentKeyring) usable() error {
	ag, closeAgent, err := s.dial()
	if err != nil {
		return err
	}
	defer func() { _ = closeAgent() }()
	_, err = sealingKey(ag)
	return err
}

func (s *sshAgentKeyring) RetrieveMasterKey() ([]byte, error) {
	return s.retrieveMasterKey(context.Background())
}

func (s *sshAgentKeyring) retrieveMasterKey(ctx context.Context) ([]byte, error) {
	data, err := s.fileSvc.ReadFile(ctx, s.path)
	if err != nil {
		if errors.Is(err, constants.ErrNotFound) {
			return nil, constants.ErrKeyStoreKeyNotFound
		}
		return nil, fmt.Errorf("ssh-agent: read sealed master key: %w", err)
	}
	var seal sshAgentSeal
	if err := json.Unmarshal(data, &seal); err != nil {
		return nil, fmt.Errorf("%w: ssh-agent seal: %w", constants.ErrKeyStoreDecodeFailed, err)
	}
	if seal.Version != sshAgentSealVersion {
		return nil, fmt.Errorf("%w: ssh-agent seal version %d", constants.ErrKeyStoreUnsupportedVersion, seal.Version)
	}
	pub, err := ssh.ParsePublicKey(seal.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("%w: ssh-agent seal public key: %w", constants.ErrKeyStoreDecodeFailed, err)
	}

	ag, closeAgent, err := s.dial()
	if err != nil {
		if errors.Is(err, constants.ErrKeyStoreSSHAgentUnavailable) {
			return nil, &SSHAgentUnlockError{Fingerprint: ssh.FingerprintSHA256(pub), Err: err}
		}
		return nil, err
	}
	defer func() { _ = closeAgent() }()
	kek, err := deriveSSHAgentKEK(ag, pub, seal.Salt)
	if err != nil {
		if errors.Is(err, constants.ErrKeyStoreSSHAgentKeyMissing) || errors.Is(err, constants.ErrKeyStoreSSHAgentUnavailable) {
			return nil, &SSHAgentUnlockError{Fingerprint: ssh.FingerprintSHA256(pub), Err: err}
		}
		return nil, err
	}
	defer vault.SecureZero(kek)
	key, err := vault.DecryptAESGCM(kek, seal.Nonce, seal.Ciphertext, seal.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("%w: ssh-agent: %w", constants.ErrInvalidCiphertext, err)
	}
	if len(key) != vault.KeySize {
		vault.SecureZero(key)
		return nil, constants.ErrKeyStoreInvalidKeyLength
	}
	return key, nil
}

func (s *sshAgentKeyring) StoreMasterKey(key []byte) error {
	if len(key) != vault.KeySize {
		return fmt.Errorf("%w: got %d, expected %d", constants.ErrKeyStoreInvalidKeyLength, len(key), vault.KeySize)
	}
	ag, closeAgent, err := s.dial()
	if err != nil {
		return err
	}
	defer func() { _ = closeAgent() }()
	pub, err := sealingKey(ag)
	if err != nil {
		return err
	}
	salt := make([]byte, sshAgentSaltSize)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("%w: %w", constants.ErrKeyStoreGenerateFailed, err)
	}
	kek, err := deriveSSHAgentKEK(ag, pub, salt)
	if err != nil {
		return err
	}
	defer vault.SecureZero(kek)
	nonce, err := vault.GenerateNonce()
	if err != nil {
		return fmt.Errorf("%w: %w", constants.ErrKeyStoreNonceGenerate, err)
	}
	ciphertext, err := vault.EncryptAESGCM(kek, nonce, key, pub.Marshal())
	if err != nil {
		return fmt.Errorf("%w: %w", constants.ErrKeyStoreCipherCreate, err)
	}
	data, err := json.Marshal(sshAgentSeal{
		Version:    sshAgentSealVersion,
		PublicKey:  pub.Marshal(),
		Salt:       salt,
		Nonce:      nonce,
		Ciphertext: ciphertext,
	})
	if err != nil {
		return fmt.Errorf("%w: %w", constants.ErrKeyStoreMarshalFailed, err)
	}
	if err := s.fileSvc.WriteFile(context.Background(), s.path, data, constants.PermFilePrivate); err != nil {
		return fmt.Errorf("ssh-agent: write sealed master key: %w", err)
	}
	return nil
}

func (s *sshAgentKeyring) DeleteMasterKey() error {
	if err := s.fileSvc.Remove(context.Background(), s.path); err != nil && !errors.Is(err, constants.ErrNotFound) {
		return fmt.Errorf("ssh-agent: delete sealed master key: %w", err)
	}
	return nil
}

// sealingKey picks the first Ed25519 key in the agent, else the first RSA key.
// ECDSA and security-key signatures are not deterministic and cannot seal.
func sealingKey(ag agent.ExtendedAgent) (ssh.PublicKey, error) {
	keys, err := ag.List()
	if err != nil {
		return nil, fmt.Errorf("%w: list keys: %w", constants.ErrKeyStoreSSHAgentUnavailable, err)
	}
	for _, keyType := range []string{ssh.KeyAlgoED25519, ssh.KeyAlgoRSA} {
		for _, k := range keys {
			if k.Type() == keyType {
				return k, nil
			}
		}
	}
	return nil, fmt.Errorf("%w: no Ed25519 or RSA key loaded", constants.ErrKeyStoreSSHAgentUnavailable)
}

// deriveSSHAgentKEK signs the salted challenge with pub's private key and
// expands the signature into a 32-byte key-encryption key.
func deriveSSHAgentKEK(ag agent.ExtendedAgent, pub ssh.PublicKey, salt []byte) ([]byte, error) {
	keys, err := ag.List()
	if err != nil {
		return nil, fmt.Errorf("%w: list keys: %w", constants.ErrKeyStoreSSHAgentUnavailable, err)
	}
	held := false
	for _, k := range keys {
		if bytes.Equal(k.Marshal(), pub.Marshal()) {
			held = true
			break
		}
	}
	if !held {
		return nil, fmt.Errorf("%w: %s", constants.ErrKeyStoreSSHAgentKeyMissing, ssh.FingerprintSHA256(pub))
	}
	var flags agent.SignatureFlags
	if pub.Type() == ssh.KeyAlgoRSA {
		flags = agent.SignatureFlagRsaSha512
	}
	challenge := append([]byte(sshAgentChallenge), salt...)
	sig, err := ag.SignWithFlags(pub, challenge, flags)
	if err != nil {
		return nil, fmt.Errorf("%w: sign: %w", constants.ErrKeyStoreSSHAgentUnavailable, err)
	}
	defer vault.SecureZero(sig.Blob)
	if err := pub.Verify(challenge, sig); err != nil {
		return nil, fmt.Errorf("%w: agent signature: %w", constants.ErrKeyStoreSSHAgentUnavailable, err)
	}
	kek, err := hkdf.Key(sha256.New, sig.Blob, salt, sshAgentChallenge+ssh.FingerprintSHA256(pub), vault.KeySize)
	if err != nil {
		return nil, fmt.Errorf("%w: derive key: %w", constants.ErrKeyStoreGenerateFailed, err)
	}
	return kek, nil
}

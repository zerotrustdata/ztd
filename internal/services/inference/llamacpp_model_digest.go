// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package inference

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/fileidentity"
	"github.com/zerotrustdata/ztd/internal/llamacpp"
)

// llamaCppDigestSettleWindow keeps a file changed this shortly before its
// hash began out of the digest cache. A filesystem with coarse modification
// times could otherwise hide a write that landed in the same tick as the
// cached identity.
const llamaCppDigestSettleWindow = time.Second

// llamaCppDigestConcurrency bounds simultaneous GGUF file hashes. Each hash
// streams a multi-gigabyte weight file.
const llamaCppDigestConcurrency = 2

// llamaCppModelDigester computes the ztd model digest of a llama-server
// model from the GGUF files under the router's --models-dir. llama-server
// reports no content digest, so the Inference Operator hashes the files it
// serves. A file digest is cached by path and reused only while the file
// keeps the same identity (same file, size, modification time, and change time).
type llamaCppModelDigester struct {
	modelRoot string
	slots     chan struct{}
	now       func() time.Time

	mu       sync.Mutex
	cached   map[string]llamaCppFileDigest
	inflight map[string]*llamaCppDigestCall
}

type llamaCppFileDigest struct {
	identity fileidentity.Identity
	digest   string
}

// llamaCppDigestCall is one in-flight file hash that concurrent callers for
// the same path wait on instead of hashing the file again.
type llamaCppDigestCall struct {
	done   chan struct{}
	digest string
	err    error
}

func newLlamaCppModelDigester(modelRoot string) *llamaCppModelDigester {
	return &llamaCppModelDigester{
		modelRoot: modelRoot,
		slots:     make(chan struct{}, llamaCppDigestConcurrency),
		now:       time.Now,
		cached:    make(map[string]llamaCppFileDigest),
		inflight:  make(map[string]*llamaCppDigestCall),
	}
}

// modelDigest resolves router model id beneath the model root and returns the
// resolved model with its ztd model digest.
func (d *llamaCppModelDigester) modelDigest(ctx context.Context, id string) (llamacpp.Model, string, error) {
	model, err := llamacpp.ResolveModel(d.modelRoot, id)
	if err != nil {
		if errors.Is(err, constants.ErrLlamaCppModelNotFound) {
			return llamacpp.Model{}, "", fmt.Errorf("llamacpp_backend: model digest: %w: %w", constants.ErrInferenceModelNotFound, err)
		}
		return llamacpp.Model{}, "", fmt.Errorf("llamacpp_backend: model digest: %w", err)
	}
	shardDigests := make([]string, 0, len(model.Shards))
	for _, shard := range model.Shards {
		digest, err := d.fileDigest(ctx, shard)
		if err != nil {
			return llamacpp.Model{}, "", err
		}
		shardDigests = append(shardDigests, digest)
	}
	digest, err := llamacpp.ComposeModelDigest(shardDigests)
	if err != nil {
		return llamacpp.Model{}, "", fmt.Errorf("llamacpp_backend: model digest: %w", err)
	}
	return model, digest, nil
}

// fileDigest returns the SHA-256 hex digest of one model file. A caller that
// finds the same path already being hashed waits for that result; if the
// hashing caller was canceled, a still-live waiter hashes the file itself.
func (d *llamaCppModelDigester) fileDigest(ctx context.Context, path string) (string, error) {
	for {
		if err := ctx.Err(); err != nil {
			return "", fmt.Errorf("llamacpp_backend: hash model file: %w", err)
		}
		identity, err := fileidentity.Stat(path)
		if err != nil {
			return "", fmt.Errorf("llamacpp_backend: model file: %w", err)
		}
		d.mu.Lock()
		if cached, ok := d.cached[path]; ok {
			if cached.identity.Equal(identity) {
				d.mu.Unlock()
				return cached.digest, nil
			}
			delete(d.cached, path)
		}
		call, joined := d.inflight[path]
		if !joined {
			call = &llamaCppDigestCall{done: make(chan struct{})}
			d.inflight[path] = call
		}
		d.mu.Unlock()

		if !joined {
			call.digest, call.err = d.hashFile(ctx, path, identity)
			d.mu.Lock()
			delete(d.inflight, path)
			d.mu.Unlock()
			close(call.done)
			return call.digest, call.err
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("llamacpp_backend: hash model file: %w", ctx.Err())
		case <-call.done:
		}
		leaderCanceled := errors.Is(call.err, context.Canceled) || errors.Is(call.err, context.DeadlineExceeded)
		if call.err == nil || !leaderCanceled || ctx.Err() != nil {
			return call.digest, call.err
		}
	}
}

// hashFile hashes path and fails closed when the file's identity at open or
// after the read differs from the identity the caller observed.
func (d *llamaCppModelDigester) hashFile(ctx context.Context, path string, identity fileidentity.Identity) (string, error) {
	select {
	case d.slots <- struct{}{}:
	case <-ctx.Done():
		return "", fmt.Errorf("llamacpp_backend: hash model file: %w", ctx.Err())
	}
	defer func() { <-d.slots }()

	startedAt := d.now()
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("llamacpp_backend: hash model file: %w", err)
	}
	defer file.Close()
	opened, err := fileidentity.Read(file)
	if err != nil {
		return "", fmt.Errorf("llamacpp_backend: hash model file: %w", err)
	}
	if !identity.Equal(opened) {
		return "", fmt.Errorf("llamacpp_backend: hash model file %q: %w", path, constants.ErrInferenceModelFileChanged)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, llamaCppContextReader{ctx: ctx, reader: file}); err != nil {
		return "", fmt.Errorf("llamacpp_backend: hash model file: %w", err)
	}
	// The open handle catches an in-place write; the path catches a rename
	// that replaced the file while it was read.
	read, err := fileidentity.Read(file)
	if err != nil {
		return "", fmt.Errorf("llamacpp_backend: hash model file: %w", err)
	}
	current, err := fileidentity.Stat(path)
	if err != nil {
		return "", fmt.Errorf("llamacpp_backend: hash model file: %w", err)
	}
	if !identity.Equal(read) || !identity.Equal(current) {
		return "", fmt.Errorf("llamacpp_backend: hash model file %q: %w", path, constants.ErrInferenceModelFileChanged)
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if startedAt.Sub(identity.LastChangedAt()) >= llamaCppDigestSettleWindow {
		d.mu.Lock()
		d.cached[path] = llamaCppFileDigest{identity: identity, digest: digest}
		d.mu.Unlock()
	}
	return digest, nil
}

// llamaCppContextReader stops a model file hash when its context ends.
type llamaCppContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r llamaCppContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

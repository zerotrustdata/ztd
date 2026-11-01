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
)

// modelFileDigestSettleWindow keeps a file changed this shortly before its
// hash began out of the digest cache. A filesystem with coarse modification
// times could otherwise hide a write that landed in the same tick as the
// cached identity.
const modelFileDigestSettleWindow = time.Second

// modelFileDigestConcurrency bounds simultaneous model artifact hashes. Each hash
// streams a multi-gigabyte weight file.
const modelFileDigestConcurrency = 2

// modelFileDigester hashes local model artifacts for provider evidence. It
// rejects changed identities, checks cancellation, and shares only settled hashes.
type modelFileDigester struct {
	component string
	slots     chan struct{}
	now       func() time.Time
	mu        sync.Mutex
	cached    map[string]modelFileDigest
	inflight  map[string]*modelFileDigestCall
}

func newModelFileDigester(component string) *modelFileDigester {
	return &modelFileDigester{
		component: component,
		slots:     make(chan struct{}, modelFileDigestConcurrency),
		now:       time.Now,
		cached:    make(map[string]modelFileDigest),
		inflight:  make(map[string]*modelFileDigestCall),
	}
}

type modelFileDigest struct {
	identity fileidentity.Identity
	digest   string
}

// modelFileDigestCall is one in-flight file hash that concurrent callers for
// the same path wait on instead of hashing the file again.
type modelFileDigestCall struct {
	done   chan struct{}
	digest string
	err    error
}

// fileDigest returns the SHA-256 hex digest of one model file. A caller that
// finds the same path already being hashed waits for that result; if the
// hashing caller was canceled, a still-live waiter hashes the file itself.
func (d *modelFileDigester) fileDigest(ctx context.Context, path string) (string, error) {
	for {
		if err := ctx.Err(); err != nil {
			return "", fmt.Errorf("%s: hash model file: %w", d.component, err)
		}
		identity, err := fileidentity.Stat(path)
		if err != nil {
			return "", fmt.Errorf("%s: model file: %w", d.component, err)
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
			call = &modelFileDigestCall{done: make(chan struct{})}
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
			return "", fmt.Errorf("%s: hash model file: %w", d.component, ctx.Err())
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
func (d *modelFileDigester) hashFile(ctx context.Context, path string, identity fileidentity.Identity) (string, error) {
	select {
	case d.slots <- struct{}{}:
	case <-ctx.Done():
		return "", fmt.Errorf("%s: hash model file: %w", d.component, ctx.Err())
	}
	defer func() { <-d.slots }()

	startedAt := d.now()
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("%s: hash model file: %w", d.component, err)
	}
	defer file.Close()
	opened, err := fileidentity.Read(file)
	if err != nil {
		return "", fmt.Errorf("%s: hash model file: %w", d.component, err)
	}
	if !identity.Equal(opened) {
		return "", fmt.Errorf("%s: hash model file %q: %w", d.component, path, constants.ErrInferenceModelFileChanged)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, modelFileContextReader{ctx: ctx, reader: file}); err != nil {
		return "", fmt.Errorf("%s: hash model file: %w", d.component, err)
	}
	// The open handle catches an in-place write; the path catches a rename
	// that replaced the file while it was read.
	read, err := fileidentity.Read(file)
	if err != nil {
		return "", fmt.Errorf("%s: hash model file: %w", d.component, err)
	}
	current, err := fileidentity.Stat(path)
	if err != nil {
		return "", fmt.Errorf("%s: hash model file: %w", d.component, err)
	}
	if !identity.Equal(read) || !identity.Equal(current) {
		return "", fmt.Errorf("%s: hash model file %q: %w", d.component, path, constants.ErrInferenceModelFileChanged)
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if startedAt.Sub(identity.LastChangedAt()) >= modelFileDigestSettleWindow {
		d.mu.Lock()
		d.cached[path] = modelFileDigest{identity: identity, digest: digest}
		d.mu.Unlock()
	}
	return digest, nil
}

// modelFileContextReader stops a model file hash when its context ends.
type modelFileContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r modelFileContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

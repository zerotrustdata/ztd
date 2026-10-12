// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package autochthon

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

// genesis is the prev hash of the first tally line.
const genesis = "0000000000000000000000000000000000000000000000000000000000000000"

// tallyLine is one line of .ztd/autochthon/tally.jsonl. Pred and Fit are the
// champion's forecast of Sense from the previous sample; Base is the
// persistence fit on the same sample. Hash is the SHA-256 of the line's JSON
// with Hash omitted, so it covers Prev and chains the file.
type tallyLine struct {
	Seq    uint64          `json:"seq"`
	Prev   string          `json:"prev"`
	At     time.Time       `json:"at"`
	Term   string          `json:"term"`
	Sense  [senseN]float32 `json:"sense"`
	Pred   [outN]float32   `json:"pred"`
	Fit    float32         `json:"fit"`
	Base   float32         `json:"base,omitempty"`
	Gen    int             `json:"gen"`
	Genome string          `json:"genome"`
	Hash   string          `json:"hash,omitempty"`
}

func (l tallyLine) digest() (string, error) {
	l.Hash = ""
	raw, err := json.Marshal(l)
	if err != nil {
		return "", fmt.Errorf("autochthon: encode tally line: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// tally is the append-only hash chain. It is not the commitment ledger.
type tally struct {
	f    *os.File
	head string
	n    uint64
}

// openTally verifies the existing chain, passing each line to visit, which
// may be nil, and opens it for append. An unterminated last line is a torn
// write and fails like a broken chain.
func openTally(ctx context.Context, files fs.RuntimeFileService, visit func(*tallyLine)) (*tally, error) {
	t, torn, err := walkTally(ctx, files, visit)
	if err != nil {
		return nil, err
	}
	if torn {
		return nil, fmt.Errorf("%w: line %d is unterminated", constants.ErrAutochthonTallyBroken, t.n)
	}
	f, err := files.OpenForAppend(ctx, constants.AutochthonTallyRelPath, constants.PermFilePrivate)
	if err != nil {
		return nil, fmt.Errorf("autochthon: open tally: %w", err)
	}
	t.f = f
	return t, nil
}

// walkTally verifies the chain from genesis and passes each line to visit,
// which may be nil. It returns the head without an open file. torn reports
// an unterminated last line, which is not verified or visited: a running
// autochthon may be writing it.
func walkTally(ctx context.Context, files fs.RuntimeFileService, visit func(*tallyLine)) (t *tally, torn bool, err error) {
	t = &tally{head: genesis}
	f, err := files.OpenForRead(ctx, constants.AutochthonTallyRelPath)
	if errors.Is(err, constants.ErrNotFound) {
		return t, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("autochthon: read tally: %w", err)
	}
	defer f.Close()
	r := bufio.NewReader(f)
	for {
		raw, err := r.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			return t, len(raw) > 0, nil
		}
		if err != nil {
			return nil, false, fmt.Errorf("autochthon: read tally: %w", err)
		}
		var line tallyLine
		if err := json.Unmarshal(raw, &line); err != nil {
			return nil, false, fmt.Errorf("%w: line %d: %w", constants.ErrAutochthonTallyBroken, t.n, err)
		}
		sum, err := line.digest()
		if err != nil {
			return nil, false, err
		}
		if line.Seq != t.n || line.Prev != t.head || line.Hash != sum {
			return nil, false, fmt.Errorf("%w: line %d", constants.ErrAutochthonTallyBroken, t.n)
		}
		if visit != nil {
			visit(&line)
		}
		t.head = line.Hash
		t.n++
	}
}

// append links line to the head and syncs it to disk.
func (t *tally) append(line tallyLine) (tallyLine, error) {
	line.Seq, line.Prev = t.n, t.head
	sum, err := line.digest()
	if err != nil {
		return tallyLine{}, err
	}
	line.Hash = sum
	raw, err := json.Marshal(line)
	if err != nil {
		return tallyLine{}, fmt.Errorf("autochthon: encode tally line: %w", err)
	}
	if _, err := t.f.Write(append(raw, '\n')); err != nil {
		return tallyLine{}, fmt.Errorf("autochthon: write tally: %w", err)
	}
	if err := t.f.Sync(); err != nil {
		return tallyLine{}, fmt.Errorf("autochthon: sync tally: %w", err)
	}
	t.head = line.Hash
	t.n++
	return line, nil
}

func (t *tally) close() error {
	return t.f.Close()
}

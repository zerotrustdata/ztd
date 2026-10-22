// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

// Package autochthon is the Gateway-less `ztd autochthon` runtime. It is not
// an Operator mode.
//
// An autochthon is sprung from the ground it stands on. It does not enroll,
// listen, or accept a GovernanceEnvelope. It samples the runtime it can see
// and breeds a fixed-topology network that tries to predict the next sample,
// starting from persistence, the forecast that nothing changes. The tally under .ztd/autochthon/ is a local hash chain. It is not
// the commitment ledger and not an EventReceipt.
package autochthon

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

// Options configure a gateway-less run.
type Options struct {
	// Interval between samples. Zero selects one second.
	Interval time.Duration
	// Ticks stops after this many tally lines. Zero runs until ctx is done.
	Ticks int
}

// Run samples the host and appends the tally until ctx is done. It opens no
// socket. The culture is saved after every generation, and the window it
// breeds on is refilled from the tally on start. One root admits one
// run: run.lock is held for the life of the run, and a second Run on the
// same root fails before it reads the tally.
func Run(ctx context.Context, files fs.RuntimeFileService, logger *slog.Logger, opts Options) error {
	lock, err := files.TryLock(ctx, constants.AutochthonLockRelPath, constants.PermFilePrivate)
	if errors.Is(err, constants.ErrRuntimeFileLocked) {
		return fmt.Errorf("%w: %s", constants.ErrAutochthonRunning, files.Resolve(constants.AutochthonDirname))
	}
	if err != nil {
		return fmt.Errorf("autochthon: lock root: %w", err)
	}
	defer lock.Close()
	var past []sense
	t, err := openTally(ctx, files, func(l *tallyLine) {
		if len(past) > windowN {
			past = past[1:]
		}
		past = append(past, sense{at: l.At, term: l.Term, v: l.Sense})
	})
	if err != nil {
		return err
	}
	defer t.close()
	c, _, err := loadCulture(ctx, files)
	if err != nil {
		return err
	}
	c.recall(past)
	logger.Info("Autochthon started; no Gateway", "dir", files.Resolve(constants.AutochthonDirname), "generation", c.gen, "tally", t.n)

	ticker := time.NewTicker(cmp.Or(opts.Interval, time.Second))
	defer ticker.Stop()
	prev := sample()
	for seen := 0; opts.Ticks == 0 || seen < opts.Ticks; seen++ {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		now := sample()
		now.since(&prev)
		r := c.step(&prev, &now)
		line, err := t.append(tallyLine{
			At:     now.at,
			Term:   now.term,
			Sense:  now.v,
			Pred:   r.pred,
			Fit:    r.fit,
			Base:   r.base,
			Gen:    r.gen,
			Genome: r.genome,
		})
		if err != nil {
			return err
		}
		if r.bred {
			if err := saveCulture(ctx, files, c); err != nil {
				return err
			}
			logger.Info("Autochthon bred", "generation", c.gen, "fit", r.episodeFit, "persistence", r.episodeBase, "seq", line.Seq)
		} else {
			logger.Debug("Autochthon tick", "generation", r.gen, "fit", r.fit, "persistence", r.base, "seq", line.Seq)
		}
		prev = now
	}
	return nil
}

// loadCulture reads culture.bin. A missing file is a new culture, reported
// with present false.
func loadCulture(ctx context.Context, files fs.RuntimeFileService) (c *culture, present bool, err error) {
	b, err := files.ReadFile(ctx, constants.AutochthonCultureRelPath)
	if errors.Is(err, constants.ErrNotFound) {
		return newCulture(), false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("autochthon: read culture: %w", err)
	}
	c, err = unmarshalCulture(b)
	return c, err == nil, err
}

func saveCulture(ctx context.Context, files fs.RuntimeFileService, c *culture) error {
	b, err := c.MarshalBinary()
	if err != nil {
		return err
	}
	if err := files.WriteFile(ctx, constants.AutochthonCultureRelPath, b, constants.PermFilePrivate); err != nil {
		return fmt.Errorf("autochthon: write culture: %w", err)
	}
	return nil
}

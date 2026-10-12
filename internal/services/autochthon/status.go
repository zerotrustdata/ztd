// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package autochthon

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

// SurpriseFit is the champion fit below which a sample is a surprise. 0.9 is
// an L1 miss of about 0.1 on the [-1, 1] target scale, about 5% of load,
// memory, or process scale combined.
const SurpriseFit = 0.9

// StatusOptions bound the report.
type StatusOptions struct {
	// Generations is how many of the most recent generations to report.
	Generations int
	// Surprises is how many of the most recent surprises to report.
	Surprises int
	// Anchor, when set, must be in the verified chain or ReadStatus fails.
	Anchor *Anchor
}

// Anchor is a recorded tally head: the hash of line Lines-1, or genesis for
// zero lines. A verified chain that still contains it has lost no line up
// to Lines, which chain verification alone cannot see for whole trailing
// lines.
type Anchor struct {
	Lines uint64 `json:"lines"`
	Head  string `json:"head"`
}

// ParseAnchor reads the <lines>:<head> form that Anchor.String writes.
func ParseAnchor(s string) (Anchor, error) {
	lines, head, ok := strings.Cut(s, ":")
	n, err := strconv.ParseUint(lines, 10, 64)
	if !ok || err != nil || !isTallyHash(head) {
		return Anchor{}, fmt.Errorf("%w: %q", constants.ErrAutochthonAnchorFormat, s)
	}
	return Anchor{Lines: n, Head: head}, nil
}

func (a Anchor) String() string {
	return strconv.FormatUint(a.Lines, 10) + ":" + a.Head
}

// isTallyHash reports whether s is a hex SHA-256 in the tally's lowercase form.
func isTallyHash(s string) bool {
	if len(s) != len(genesis) {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// AnchorStatus is an anchor checked against the verified chain. A Status
// only carries one that holds.
type AnchorStatus struct {
	Anchor
	Holds bool `json:"holds"`
}

// Status is a read-only report on one autochthon root. A Status is only
// returned for a tally whose hash chain verified from genesis.
type Status struct {
	Dir         string             `json:"dir"`
	Tally       TallyStatus        `json:"tally"`
	Culture     CultureStatus      `json:"culture"`
	Generations []GenerationStatus `json:"generations"`
	Surprises   []Surprise         `json:"surprises"`
	Anchor      *AnchorStatus      `json:"anchor,omitempty"`
}

// TallyStatus summarizes the verified chain. Fit and Base are means over
// the scored lines, the lines that carry a persistence fit.
type TallyStatus struct {
	Lines     uint64     `json:"lines"`
	Head      string     `json:"head"`
	First     *time.Time `json:"first,omitempty"`
	Last      *time.Time `json:"last,omitempty"`
	Pending   bool       `json:"pending"`
	Scored    uint64     `json:"scored"`
	Fit       float32    `json:"fit"`
	Base      float32    `json:"base"`
	Surprises uint64     `json:"surprises"`
}

// CultureStatus describes culture.bin. A missing file is a culture that has
// not finished its first episode. Persistence reports that the champion is
// still the zero genome: nothing has beaten assuming the host stays put.
type CultureStatus struct {
	Present     bool   `json:"present"`
	Generation  int    `json:"generation"`
	Champion    string `json:"champion"`
	Persistence bool   `json:"persistence"`
}

// GenerationStatus is one generation's champion against persistence.
// Samples can exceed one episode when a restart repeated the generation.
type GenerationStatus struct {
	Gen     int     `json:"gen"`
	Samples int     `json:"samples"`
	Fit     float32 `json:"fit"`
	Base    float32 `json:"base"`
}

// Surprise is a sample the champion missed by more than SurpriseFit allows.
type Surprise struct {
	Seq   uint64    `json:"seq"`
	At    time.Time `json:"at"`
	Load  float32   `json:"load"`
	Mem   float32   `json:"mem"`
	Procs float32   `json:"procs"`
	Fit   float32   `json:"fit"`
	Base  float32   `json:"base"`
}

// ReadStatus verifies the tally and reads the culture without writing. A
// broken chain, a missing anchor, or an unreadable culture is an error, not
// a report. A pending line never satisfies an anchor.
func ReadStatus(ctx context.Context, files fs.RuntimeFileService, opts StatusOptions) (*Status, error) {
	st := &Status{Dir: files.Resolve(constants.AutochthonDirname), Generations: []GenerationStatus{}, Surprises: []Surprise{}}
	var fit, base float64
	anchored := genesis
	t, torn, err := walkTally(ctx, files, func(l *tallyLine) {
		if opts.Anchor != nil && l.Seq+1 == opts.Anchor.Lines {
			anchored = l.Hash
		}
		at := l.At
		if st.Tally.First == nil {
			st.Tally.First = &at
		}
		st.Tally.Last = &at
		if l.Base == 0 {
			return
		}
		st.Tally.Scored++
		fit += float64(l.Fit)
		base += float64(l.Base)
		if n := len(st.Generations); n > 0 && st.Generations[n-1].Gen == l.Gen {
			g := &st.Generations[n-1]
			g.Fit += (l.Fit - g.Fit) / float32(g.Samples+1)
			g.Base += (l.Base - g.Base) / float32(g.Samples+1)
			g.Samples++
		} else {
			st.Generations = keepLast(append(st.Generations, GenerationStatus{Gen: l.Gen, Samples: 1, Fit: l.Fit, Base: l.Base}), opts.Generations)
		}
		if l.Fit < SurpriseFit {
			st.Tally.Surprises++
			st.Surprises = keepLast(append(st.Surprises, Surprise{
				Seq: l.Seq, At: l.At, Load: l.Sense[0], Mem: l.Sense[1], Procs: l.Sense[6], Fit: l.Fit, Base: l.Base,
			}), opts.Surprises)
		}
	})
	if err != nil {
		return nil, err
	}
	st.Tally.Lines, st.Tally.Head, st.Tally.Pending = t.n, t.head, torn
	if a := opts.Anchor; a != nil {
		if a.Lines > t.n || anchored != a.Head {
			return nil, fmt.Errorf("%w: %s (tally has %d verified lines)", constants.ErrAutochthonAnchorMissing, a, t.n)
		}
		st.Anchor = &AnchorStatus{Anchor: *a, Holds: true}
	}
	if st.Tally.Scored > 0 {
		st.Tally.Fit = float32(fit / float64(st.Tally.Scored))
		st.Tally.Base = float32(base / float64(st.Tally.Scored))
	}

	c, present, err := loadCulture(ctx, files)
	if err != nil {
		return nil, err
	}
	st.Culture = CultureStatus{Present: present, Generation: c.gen, Champion: c.pop[0].hash(), Persistence: c.pop[0] == genome{}}
	return st, nil
}

// keepLast trims s to its last n elements. n <= 0 keeps none.
func keepLast[T any](s []T, n int) []T {
	if len(s) <= max(n, 0) {
		return s
	}
	return append(s[:0:0], s[len(s)-max(n, 0):]...)
}

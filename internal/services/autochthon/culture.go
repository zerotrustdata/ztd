// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package autochthon

import (
	"cmp"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// Fixed topology: inputN → hiddenN → outN, tanh on both layers, plus direct
// input → outN connections summed into the output layer. The output is the
// change from the current sample, so the zero genome is persistence: it
// predicts that the next sample equals this one. The direct connections let
// one mutation from persistence make a linear predictor; through the hidden
// layer alone, a useful first move needs W1 and W2 to mutate together.
const (
	hiddenN  = 8
	outN     = 4
	popN     = 16
	eliteN   = 2
	episodeN = 32
	windowN  = 4 * episodeN

	offB1   = hiddenN * inputN
	offW2   = offB1 + hiddenN
	offB2   = offW2 + outN*hiddenN
	offD    = offB2 + outN
	genomeN = offD + outN*inputN

	// Each child mutates about one gene in fifty. Its step is sigma halved
	// 0 to sigmaOctaves-1 times, so a generation tries both coarse moves and
	// moves near the scale of a host's change from one second to the next.
	sigma        = 2
	sigmaOctaves = 8
	mutateP      = 0.02
	initScale    = 0.1
	geneLimit    = 8

	cultureSeed    = 7
	cultureMagic   = "ZTDA"
	cultureVersion = 4
	cultureHeaderN = 12
	cultureSize    = cultureHeaderN + popN*genomeN*4
)

// genome is one network's weights packed as W1, B1, W2, B2, D.
type genome [genomeN]float32

// forecast predicts the next target from the current sample.
func (g *genome) forecast(now *sense) [outN]float32 {
	base := now.target()
	x := now.input()
	y := g.infer(&x)
	for i := range y {
		y[i] = min(max(base[i]+y[i], -1), 1)
	}
	return y
}

func (g *genome) infer(x *[inputN]float32) (y [outN]float32) {
	var h [hiddenN]float32
	dense(g[:offB1], g[offB1:offW2], x[:], h[:], true)
	var s [outN]float32
	dense(g[offW2:offB2], g[offB2:offD], h[:], s[:], false)
	dense(g[offD:], s[:], x[:], y[:], true)
	return y
}

func dense(w, b, x, y []float32, squash bool) {
	for i := range y {
		s := b[i]
		row := w[i*len(x) : (i+1)*len(x)]
		for j, xj := range x {
			s += row[j] * xj
		}
		if squash {
			s = float32(math.Tanh(float64(s)))
		}
		y[i] = s
	}
}

func (g *genome) appendTo(b []byte) []byte {
	for _, v := range g {
		b = binary.LittleEndian.AppendUint32(b, math.Float32bits(v))
	}
	return b
}

func (g *genome) hash() string {
	sum := sha256.Sum256(g.appendTo(make([]byte, 0, genomeN*4)))
	return hex.EncodeToString(sum[:])
}

// culture is one population of predictors on one host. pop[0] is the
// champion: the persistence seed until the first breed, then the fittest
// genome over the window at the last breed. window holds the last windowN
// (prev, next) pairs; fit and base sum the champion's and persistence's
// closeness over the episode in progress.
type culture struct {
	gen    int
	tick   int
	pop    [popN]genome
	window [][2]sense
	fit    float32
	base   float32
	rng    *rand.Rand
}

// stepResult is the champion's forecast. base is the persistence fit on the
// same sample, the floor the champion is measured against. When bred is set,
// episodeFit and episodeBase are their means over the finished episode.
type stepResult struct {
	pred        [outN]float32
	fit         float32
	base        float32
	gen         int
	genome      string
	bred        bool
	episodeFit  float32
	episodeBase float32
}

func newRNG(seed uint64) *rand.Rand {
	return rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)) //nolint:gosec // seeded mutation noise, not a secret
}

// newCulture seeds pop[0] with the zero genome, persistence, and the rest
// with small perturbations of it.
func newCulture() *culture {
	c := &culture{rng: newRNG(cultureSeed)}
	for i := 1; i < popN; i++ {
		for j := range c.pop[i] {
			c.pop[i][j] = float32(c.rng.NormFloat64()) * initScale
		}
	}
	return c
}

// step scores the champion, which was chosen before next was seen, and
// persistence on predicting next from prev, and adds the pair to the window.
// A finished episode breeds before step returns.
func (c *culture) step(prev, next *sense) stepResult {
	target := next.target()
	base := prev.target()
	r := stepResult{base: closeness(&base, &target), gen: c.gen, genome: c.pop[0].hash()}
	r.pred = c.pop[0].forecast(prev)
	r.fit = closeness(&r.pred, &target)
	c.fit += r.fit
	c.base += r.base
	c.remember(prev, next)
	c.tick++
	if c.tick >= episodeN {
		r.episodeFit, r.episodeBase = c.fit/episodeN, c.base/episodeN
		c.breed()
		r.bred = true
	}
	return r
}

func (c *culture) remember(prev, next *sense) {
	if len(c.window) == windowN {
		c.window = c.window[1:]
	}
	c.window = append(c.window, [2]sense{*prev, *next})
}

// recall fills the window from past samples, oldest first, so a restarted
// culture breeds on the same window it would have had without the restart.
// The first sample is only a predecessor.
func (c *culture) recall(past []sense) {
	for i := 1; i < len(past); i++ {
		past[i].since(&past[i-1])
		c.remember(&past[i-1], &past[i])
	}
}

// closeness is exp(-L1 error), in (0, 1].
func closeness(pred, target *[outN]float32) float32 {
	var e float64
	for i := range target {
		e += math.Abs(float64(pred[i] - target[i]))
	}
	return float32(math.Exp(-e))
}

// breed scores every genome over the window, keeps the eliteN best, fittest
// first, and fills the rest with
// mutated copies of parents from the top quarter. There is no crossover:
// hidden units of unrelated genomes do not line up. Persistence is a standing
// contestant: when no elite is the zero genome it takes the next slot
// unmutated, so a champion that falls below persistence is replaced by it.
// Scoring on the window rather than the last episode alone keeps a burst that
// ended with the episode from choosing the next champion, and scores elites
// and new children on the same samples.
func (c *culture) breed() {
	var fit [popN]float32
	for i := range c.pop {
		for _, w := range c.window {
			target := w[1].target()
			y := c.pop[i].forecast(&w[0])
			fit[i] += closeness(&y, &target)
		}
	}
	var order [popN]int
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order[:], func(a, b int) int { return cmp.Compare(fit[b], fit[a]) })
	pool := order[:max(popN/4, 1)]

	var next [popN]genome
	for i := range eliteN {
		next[i] = c.pop[order[i]]
	}
	children := eliteN
	if !slices.Contains(next[:eliteN], genome{}) {
		next[children] = genome{}
		children++
	}
	for i := children; i < popN; i++ {
		next[i] = c.pop[pool[c.rng.IntN(len(pool))]]
		step := sigma * float32(math.Exp2(-float64(c.rng.IntN(sigmaOctaves))))
		for g := range next[i] {
			if c.rng.Float64() < mutateP {
				next[i][g] = min(max(next[i][g]+float32(c.rng.NormFloat64())*step, -geneLimit), geneLimit)
			}
		}
	}
	c.pop = next
	c.fit = 0
	c.base = 0
	c.tick = 0
	c.gen++
}

// MarshalBinary writes the population at an episode boundary. Fitness and
// the in-progress episode are not persisted.
func (c *culture) MarshalBinary() ([]byte, error) {
	b := make([]byte, 0, cultureSize)
	b = append(b, cultureMagic...)
	b = binary.LittleEndian.AppendUint32(b, cultureVersion)
	b = binary.LittleEndian.AppendUint32(b, uint32(c.gen))
	for i := range c.pop {
		b = c.pop[i].appendTo(b)
	}
	return b, nil
}

// unmarshalCulture restores a population. The RNG is reseeded from the
// generation so a restart does not replay the same mutations. A gene that is
// not finite or outside geneLimit is a format error.
func unmarshalCulture(b []byte) (*culture, error) {
	if len(b) != cultureSize || string(b[:4]) != cultureMagic || binary.LittleEndian.Uint32(b[4:]) != cultureVersion {
		return nil, fmt.Errorf("%w: %d bytes", constants.ErrAutochthonCultureFormat, len(b))
	}
	gen := binary.LittleEndian.Uint32(b[8:])
	c := &culture{gen: int(gen), rng: newRNG(uint64(gen) + cultureSeed)}
	b = b[cultureHeaderN:]
	for i := range c.pop {
		for j := range c.pop[i] {
			v := math.Float32frombits(binary.LittleEndian.Uint32(b))
			if !(v >= -geneLimit && v <= geneLimit) {
				return nil, fmt.Errorf("%w: gene %d of genome %d", constants.ErrAutochthonCultureFormat, j, i)
			}
			c.pop[i][j] = v
			b = b[4:]
		}
	}
	return c, nil
}

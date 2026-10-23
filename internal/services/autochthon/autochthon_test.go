// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package autochthon

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func newTestFiles(t *testing.T) fs.RuntimeFileService {
	t.Helper()
	files, err := fs.NewRuntimeFileService(testutil.TempDir(t), testutil.NewTestLogger())
	require.NoError(t, err)
	return files
}

func TestGenomeLayout(t *testing.T) {
	assert.Equal(t, 248, genomeN)
	assert.Equal(t, cultureHeaderN+popN*genomeN*4, cultureSize)
	target := [outN]float32{0.2, -0.4, 0.1, 0.8}
	assert.InDelta(t, 1, closeness(&target, &target), 1e-6)
}

func TestTermUnit(t *testing.T) {
	assert.Zero(t, termUnit(""))
	a, b := termUnit("xterm-256color"), termUnit("dumb")
	assert.NotEqual(t, a, b)
	for _, v := range []float32{a, b} {
		assert.Greater(t, v, float32(0))
		assert.LessOrEqual(t, v, float32(1))
	}
	assert.Equal(t, a, termUnit("xterm-256color"))
}

func TestZeroGenomeIsPersistence(t *testing.T) {
	var g genome
	s := sense{v: [senseN]float32{0.2, 0.4, termUnit("dumb"), 0.5, 0.1, 0.2, 0.3, 1}, d: [outN]float32{0.1, -0.2, 0, 0.3}}
	assert.Equal(t, s.target(), g.forecast(&s))
}

func TestCultureHoldsAConstantHostAndRoundTrips(t *testing.T) {
	c := newCulture()
	s := sense{term: "dumb", v: [senseN]float32{0.2, 0.4, termUnit("dumb"), 0.5, 0.1, 0.2, 0.3, 1}}

	for range 12 * episodeN {
		r := c.step(&s, &s)
		assert.InDelta(t, 1, r.base, 1e-6)
		assert.InDelta(t, r.base, r.fit, 1e-6, "the champion is never worse than persistence on a constant host")
	}
	assert.Equal(t, 12, c.gen)

	b, err := c.MarshalBinary()
	require.NoError(t, err)
	loaded, err := unmarshalCulture(b)
	require.NoError(t, err)
	assert.Equal(t, c.gen, loaded.gen)
	assert.Equal(t, c.pop, loaded.pop)

	_, err = unmarshalCulture(b[:len(b)-1])
	assert.ErrorIs(t, err, constants.ErrAutochthonCultureFormat)
	binary.LittleEndian.PutUint32(b[cultureHeaderN:], math.Float32bits(float32(math.NaN())))
	_, err = unmarshalCulture(b)
	assert.ErrorIs(t, err, constants.ErrAutochthonCultureFormat)
}

// An oscillating load is the simplest host persistence cannot predict. The
// champion's episode fit must never fall and must end above persistence.
func TestCultureLearnsAnOscillatingHost(t *testing.T) {
	c := newCulture()
	lo := sense{v: [senseN]float32{0.2, 0.4, 0, 0.5, 0.1, 0.2, 0.3, 1}}
	hi := lo
	hi.v[0] = 0.6

	var first, last, base float32
	for i := range 200 * episodeN {
		prev, next := &lo, &hi
		if i%2 == 1 {
			prev, next = &hi, &lo
		}
		r := c.step(prev, next)
		if !r.bred {
			continue
		}
		require.GreaterOrEqual(t, r.episodeFit, last, "generation %d", r.gen)
		if first == 0 {
			first = r.episodeFit
		}
		last, base = r.episodeFit, r.episodeBase
	}
	assert.InDelta(t, base, first, 1e-6, "the first champion is persistence")
	assert.Greater(t, last, base+0.15)
}

// A real host changes by about 0.002 a second. The culture must find a
// process count that flips by that much each sample, which persistence
// always misses, and keep most of the skill a perfect forecast would have.
func TestCultureLearnsAFineOscillation(t *testing.T) {
	const amp = 0.002
	c := newCulture()
	prev := sense{v: [senseN]float32{0.2, 0.4, 0, 0.5, 0.1, 0.2, 0.3, 1}}
	var skill float32
	var n int
	for i := range 300 * episodeN {
		next := prev
		next.v[6] = 0.3 + amp*float32(i%2)
		next.since(&prev)
		r := c.step(&prev, &next)
		prev = next
		if r.bred && r.gen >= 200 {
			skill += r.episodeFit - r.episodeBase
			n++
		}
	}
	perfect := 1 - float32(math.Exp(-2*amp))
	assert.Greater(t, skill/float32(n), perfect*0.75)
}

// A load past the CPUs still moves the load sense.
func TestLoadSenseDoesNotSaturate(t *testing.T) {
	assert.Zero(t, loadSense(0, 8))
	assert.InDelta(t, 0.5, loadSense(8, 8), 1e-6)
	assert.Greater(t, loadSense(16, 8), loadSense(15.9, 8))
	assert.Less(t, loadSense(1000, 8), float32(1))
}

// The kernel refreshes the load average every five seconds, moving it a
// fixed fraction toward the run queue. Between refreshes the load does not
// change, so the change since the previous sample says nothing about the
// next refresh; the last load change and its age do. With run-queue bursts
// to twice the CPUs that come and go, the culture must beat persistence on
// the rises and decays.
func TestCultureLearnsTheLoadRefresh(t *testing.T) {
	decay := math.Exp(-5.0 / 60)
	var load, skill float64
	var n int
	c := newCulture()
	prev := sense{v: [senseN]float32{0, 0.4, 0, 0.5, 0.1, 0.2, 0.3, 1}}
	for i := range 300 * episodeN {
		runQueue := 0.5
		if i%400 < 120 {
			runQueue = 16
		}
		if i%5 == 0 {
			load = load*decay + runQueue*(1-decay)
		}
		next := prev
		next.v[0] = loadSense(math.Round(load*100)/100, 8)
		next.since(&prev)
		r := c.step(&prev, &next)
		prev = next
		if r.bred && r.gen >= 200 {
			skill += float64(r.episodeFit - r.episodeBase)
			n++
		}
	}
	assert.Greater(t, skill/float64(n), 0.003, "without the load age the culture stays on persistence here")
}

// A host that stops oscillating makes the learned champion worse than
// persistence. Persistence must still be in the population to take the
// champion back once quiet samples outweigh the oscillation in the window,
// and keep it from then on.
func TestCultureFallsBackToPersistenceAfterARegimeChange(t *testing.T) {
	c := newCulture()
	lo := sense{v: [senseN]float32{0.2, 0.4, 0, 0.5, 0.1, 0.2, 0.3, 1}}
	hi := lo
	hi.v[0] = 0.6
	for i := range 200 * episodeN {
		prev, next := &lo, &hi
		if i%2 == 1 {
			prev, next = &hi, &lo
		}
		c.step(prev, next)
	}
	require.NotEqual(t, genome{}, c.pop[0], "the oscillating host replaced persistence as champion")

	var quiet []stepResult
	for range 2 * windowN {
		if r := c.step(&lo, &lo); r.bred {
			quiet = append(quiet, r)
		}
	}
	assert.Less(t, quiet[0].episodeFit, quiet[0].episodeBase, "the learned champion predicts a change that does not come")
	back := slices.IndexFunc(quiet, func(r stepResult) bool { return r.episodeFit == r.episodeBase })
	require.GreaterOrEqual(t, back, 1, "persistence never took the champion back")
	assert.LessOrEqual(t, back, windowN/episodeN)
	for _, r := range quiet[back:] {
		assert.InDelta(t, r.episodeBase, r.episodeFit, 1e-6, "generation %d: persistence is champion", r.gen)
	}
	assert.Equal(t, genome{}, c.pop[0])
}

// A restart refills the window from the tally: the last windowN pairs, with
// each sample's change taken from the one before it.
func TestCultureRecallsTheWindow(t *testing.T) {
	past := make([]sense, windowN+10)
	for i := range past {
		past[i].v[6] = float32(i%3) / 4
	}
	c := newCulture()
	c.recall(past)
	require.Len(t, c.window, windowN)
	for i, w := range c.window {
		require.Equal(t, w[0].v, past[i+9].v)
		assert.InDelta(t, w[1].target()[3]-w[0].target()[3], w[1].d[3], 1e-6)
	}
}

func TestTallyChainsAndReplays(t *testing.T) {
	ctx := context.Background()
	files := newTestFiles(t)

	tl, err := openTally(ctx, files, nil)
	require.NoError(t, err)
	first, err := tl.append(tallyLine{At: time.Unix(0, 1).UTC(), Term: "xterm", Fit: 0.5, Genome: "aa"})
	require.NoError(t, err)
	assert.Equal(t, genesis, first.Prev)
	assert.Zero(t, first.Seq)
	second, err := tl.append(tallyLine{At: time.Unix(0, 2).UTC(), Term: "xterm", Fit: 0.6, Genome: "bb"})
	require.NoError(t, err)
	assert.Equal(t, first.Hash, second.Prev)
	assert.EqualValues(t, 1, second.Seq)
	require.NoError(t, tl.close())

	again, err := openTally(ctx, files, nil)
	require.NoError(t, err)
	assert.EqualValues(t, 2, again.n)
	assert.Equal(t, second.Hash, again.head)
	require.NoError(t, again.close())

	raw, err := files.ReadFile(ctx, constants.AutochthonTallyRelPath)
	require.NoError(t, err)
	raw[len(raw)-10] ^= 1
	require.NoError(t, files.WriteFile(ctx, constants.AutochthonTallyRelPath, raw, constants.PermFilePrivate))
	_, err = openTally(ctx, files, nil)
	assert.ErrorIs(t, err, constants.ErrAutochthonTallyBroken)
}

func TestRunWritesTallyAndCulture(t *testing.T) {
	ctx := context.Background()
	files := newTestFiles(t)

	require.NoError(t, Run(ctx, files, testutil.NewTestLogger(), Options{Interval: time.Millisecond, Ticks: episodeN}))

	tl, err := openTally(ctx, files, nil)
	require.NoError(t, err)
	assert.EqualValues(t, episodeN, tl.n)
	require.NoError(t, tl.close())

	c, present, err := loadCulture(ctx, files)
	require.NoError(t, err)
	assert.True(t, present)
	assert.Equal(t, 1, c.gen)
}

func TestReadStatusReportsSkillAndSurprises(t *testing.T) {
	ctx := context.Background()
	files := newTestFiles(t)

	tl, err := openTally(ctx, files, nil)
	require.NoError(t, err)
	for i, l := range []tallyLine{
		{Gen: 0, Fit: 0.95, Base: 0.95},
		{Gen: 0, Fit: 0.85, Base: 0.75, Sense: [senseN]float32{0.5, 0.25, 0, 0, 0, 0, 0.6, 1}},
		{Gen: 1, Fit: 0.99, Base: 0.97},
		{Gen: 2, Fit: 0.98, Base: 0.98},
	} {
		l.At = time.Unix(int64(i), 0).UTC()
		_, err := tl.append(l)
		require.NoError(t, err)
	}
	require.NoError(t, tl.close())
	c := newCulture()
	c.gen = 3
	require.NoError(t, saveCulture(ctx, files, c))

	st, err := ReadStatus(ctx, files, StatusOptions{Generations: 2, Surprises: 5})
	require.NoError(t, err)
	assert.EqualValues(t, 4, st.Tally.Lines)
	assert.EqualValues(t, 4, st.Tally.Scored)
	assert.False(t, st.Tally.Pending)
	assert.InDelta(t, 0.9425, st.Tally.Fit, 1e-6)
	assert.InDelta(t, 0.9125, st.Tally.Base, 1e-6)
	assert.Equal(t, time.Unix(3, 0).UTC(), *st.Tally.Last)
	assert.Equal(t, []GenerationStatus{{Gen: 1, Samples: 1, Fit: 0.99, Base: 0.97}, {Gen: 2, Samples: 1, Fit: 0.98, Base: 0.98}}, st.Generations)
	assert.EqualValues(t, 1, st.Tally.Surprises)
	require.Len(t, st.Surprises, 1)
	assert.Equal(t, Surprise{Seq: 1, At: time.Unix(1, 0).UTC(), Load: 0.5, Mem: 0.25, Procs: 0.6, Fit: 0.85, Base: 0.75}, st.Surprises[0])
	assert.Equal(t, CultureStatus{Present: true, Generation: 3, Champion: c.pop[0].hash(), Persistence: true}, st.Culture)
}

func TestReadStatusPendingLineAndBrokenChain(t *testing.T) {
	ctx := context.Background()
	files := newTestFiles(t)

	tl, err := openTally(ctx, files, nil)
	require.NoError(t, err)
	_, err = tl.append(tallyLine{Fit: 0.9, Base: 0.9})
	require.NoError(t, err)
	_, err = tl.f.WriteString(`{"seq":1,"prev":`)
	require.NoError(t, err)
	require.NoError(t, tl.close())

	st, err := ReadStatus(ctx, files, StatusOptions{})
	require.NoError(t, err, "a line being written is pending, not broken")
	assert.True(t, st.Tally.Pending)
	assert.EqualValues(t, 1, st.Tally.Lines)
	assert.Empty(t, st.Generations)
	_, err = openTally(ctx, files, nil)
	assert.ErrorIs(t, err, constants.ErrAutochthonTallyBroken, "start refuses a torn tail")

	require.NoError(t, files.WriteFile(ctx, constants.AutochthonTallyRelPath, []byte("{}\n"), constants.PermFilePrivate))
	_, err = ReadStatus(ctx, files, StatusOptions{})
	assert.ErrorIs(t, err, constants.ErrAutochthonTallyBroken)
}

func TestReadStatusOfAnEmptyRootWritesNothing(t *testing.T) {
	ctx := context.Background()
	files := newTestFiles(t)

	st, err := ReadStatus(ctx, files, StatusOptions{Generations: 10, Surprises: 10})
	require.NoError(t, err)
	assert.Zero(t, st.Tally.Lines)
	assert.Equal(t, genesis, st.Tally.Head)
	assert.Nil(t, st.Tally.Last)
	assert.False(t, st.Culture.Present)
	assert.NotNil(t, st.Generations)
	assert.NotNil(t, st.Surprises)
	exists, err := files.FileExists(ctx, constants.AutochthonDirname)
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestRunRefusesARootThatIsAlreadyRunning(t *testing.T) {
	ctx := context.Background()
	files := newTestFiles(t)

	held, err := files.TryLock(ctx, constants.AutochthonLockRelPath, constants.PermFilePrivate)
	require.NoError(t, err)
	err = Run(ctx, files, testutil.NewTestLogger(), Options{Interval: time.Millisecond, Ticks: 1})
	assert.ErrorIs(t, err, constants.ErrAutochthonRunning)
	exists, err := files.FileExists(ctx, constants.AutochthonTallyRelPath)
	require.NoError(t, err)
	assert.False(t, exists, "a refused run touches no tally")

	require.NoError(t, held.Close())
	require.NoError(t, Run(ctx, files, testutil.NewTestLogger(), Options{Interval: time.Millisecond, Ticks: 1}))
}

func TestReadStatusAnchor(t *testing.T) {
	ctx := context.Background()
	files := newTestFiles(t)

	st, err := ReadStatus(ctx, files, StatusOptions{Anchor: &Anchor{Lines: 0, Head: genesis}})
	require.NoError(t, err, "the genesis anchor holds on an empty tally")
	assert.Equal(t, &AnchorStatus{Anchor: Anchor{Head: genesis}, Holds: true}, st.Anchor)

	tl, err := openTally(ctx, files, nil)
	require.NoError(t, err)
	var lines []tallyLine
	for range 3 {
		l, err := tl.append(tallyLine{Fit: 0.9, Base: 0.9})
		require.NoError(t, err)
		lines = append(lines, l)
	}
	_, err = tl.f.WriteString(`{"seq":3,"prev":`)
	require.NoError(t, err)
	require.NoError(t, tl.close())

	for _, a := range []Anchor{{Lines: 0, Head: genesis}, {Lines: 2, Head: lines[1].Hash}, {Lines: 3, Head: lines[2].Hash}} {
		st, err := ReadStatus(ctx, files, StatusOptions{Anchor: &a})
		require.NoError(t, err, "anchor %s", a)
		assert.True(t, st.Anchor.Holds)
	}
	for _, a := range []Anchor{{Lines: 2, Head: lines[2].Hash}, {Lines: 4, Head: lines[2].Hash}, {Lines: 1, Head: genesis}} {
		_, err := ReadStatus(ctx, files, StatusOptions{Anchor: &a})
		assert.ErrorIs(t, err, constants.ErrAutochthonAnchorMissing, "anchor %s", a)
	}

	raw, err := files.ReadFile(ctx, constants.AutochthonTallyRelPath)
	require.NoError(t, err)
	cut := raw[:bytes.LastIndexByte(raw[:bytes.LastIndex(raw, []byte(`{"seq":2`))], '\n')+1]
	require.NoError(t, files.WriteFile(ctx, constants.AutochthonTallyRelPath, cut, constants.PermFilePrivate))
	st, err = ReadStatus(ctx, files, StatusOptions{})
	require.NoError(t, err, "a tally cut at a line boundary still verifies")
	assert.EqualValues(t, 2, st.Tally.Lines)
	_, err = ReadStatus(ctx, files, StatusOptions{Anchor: &Anchor{Lines: 3, Head: lines[2].Hash}})
	assert.ErrorIs(t, err, constants.ErrAutochthonAnchorMissing, "the anchor sees the removed line")
}

func TestParseAnchor(t *testing.T) {
	a, err := ParseAnchor("133:" + strings.Repeat("ab", 32))
	require.NoError(t, err)
	assert.Equal(t, Anchor{Lines: 133, Head: strings.Repeat("ab", 32)}, a)
	assert.Equal(t, "133:"+strings.Repeat("ab", 32), a.String())

	for _, s := range []string{
		"", strings.Repeat("ab", 32), "x:" + genesis, "-1:" + genesis, "1:" + genesis[1:], "1:" + strings.Repeat("AB", 32), "1:" + genesis + "0",
	} {
		_, err := ParseAnchor(s)
		assert.ErrorIs(t, err, constants.ErrAutochthonAnchorFormat, "%q", s)
	}
}

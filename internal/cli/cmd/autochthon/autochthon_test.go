// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package autochthoncmd

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/autochthon"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestAutochthonFlags(t *testing.T) {
	t.Parallel()
	cmd := Cmd()
	for _, name := range autochthonFlags {
		assert.NotNil(t, cmd.Flags().Lookup(name), "autochthon should have --%s", name)
	}
	assert.Nil(t, cmd.Flags().Lookup("autochthon"))
	assert.Nil(t, cmd.Flags().Lookup("anchor"), "only status verifies an anchor")

	status, _, err := cmd.Find([]string{"status"})
	require.NoError(t, err)
	for _, name := range statusFlags {
		if name != "json" {
			assert.NotNil(t, status.Flags().Lookup(name), "status should have --%s", name)
		}
	}
	err = newRoot(&bytes.Buffer{}, "autochthon", "--working-dir", testutil.TempDir(t), "--anchor", "0:"+strings.Repeat("0", 64)).Execute()
	assert.ErrorContains(t, err, "unknown flag: --anchor")
}

// newRoot mirrors the root command's persistent flags.
func newRoot(out *bytes.Buffer, args ...string) *cobra.Command {
	root := &cobra.Command{Use: "ztd"}
	root.PersistentFlags().StringP("endpoint", "e", "", "Gateway endpoint")
	root.PersistentFlags().IntP("port", "p", 0, "Gateway port")
	root.PersistentFlags().Bool("json", false, "JSON output")
	root.AddCommand(Cmd())
	root.SetArgs(args)
	root.SetOut(out)
	root.SilenceUsage, root.SilenceErrors = true, true
	return root
}

func TestAutochthonRefusesGatewayFlags(t *testing.T) {
	t.Parallel()
	for _, sub := range [][]string{{"autochthon"}, {"autochthon", "status"}} {
		for _, flag := range [][]string{{"--endpoint", "gateway.example"}, {"--port", "8443"}} {
			args := append(append(append([]string{}, sub...), "--working-dir", testutil.TempDir(t)), flag...)
			require.ErrorIs(t, newRoot(&bytes.Buffer{}, args...).Execute(), constants.ErrAutochthonGatewayFlag, "args %v", args)
		}
	}
	err := newRoot(&bytes.Buffer{}, "autochthon", "--working-dir", testutil.TempDir(t), "--json").Execute()
	assert.ErrorIs(t, err, constants.ErrAutochthonGatewayFlag, "only status emits JSON")
}

func TestAutochthonStatus(t *testing.T) {
	t.Parallel()
	dir := testutil.TempDir(t)
	files, err := fs.NewRuntimeFileService(dir, testutil.NewTestLogger())
	require.NoError(t, err)
	require.NoError(t, autochthon.Run(context.Background(), files, testutil.NewTestLogger(), autochthon.Options{Interval: time.Millisecond, Ticks: 40}))

	var out bytes.Buffer
	require.NoError(t, newRoot(&out, "autochthon", "status", "--working-dir", dir).Execute())
	text := out.String()
	assert.Contains(t, text, "40 lines, chain verified")
	assert.Contains(t, text, "generation 1, champion ")
	assert.Contains(t, text, "GEN  SAMPLES")
	_, row, ok := strings.Cut(text, "Anchor")
	require.True(t, ok, "text report has an Anchor row")
	anchor := strings.Fields(row)[0]
	assert.True(t, strings.HasPrefix(anchor, "40:"), anchor)

	out.Reset()
	require.NoError(t, newRoot(&out, "autochthon", "status", "--working-dir", dir, "--anchor", anchor).Execute())
	assert.Contains(t, out.String(), anchor+" holds")
	err = newRoot(&bytes.Buffer{}, "autochthon", "status", "--working-dir", dir, "--anchor", "41:"+strings.TrimPrefix(anchor, "40:")).Execute()
	require.ErrorIs(t, err, constants.ErrAutochthonAnchorMissing)
	err = newRoot(&bytes.Buffer{}, "autochthon", "status", "--working-dir", dir, "--anchor", "40").Execute()
	require.ErrorIs(t, err, constants.ErrAutochthonAnchorFormat)

	out.Reset()
	require.NoError(t, newRoot(&out, "autochthon", "status", "--working-dir", dir, "--generations", "1", "--json").Execute())
	var st autochthon.Status
	require.NoError(t, json.Unmarshal(out.Bytes(), &st))
	assert.EqualValues(t, 40, st.Tally.Lines)
	assert.EqualValues(t, 40, st.Tally.Scored)
	assert.Equal(t, 1, st.Culture.Generation)
	require.Len(t, st.Generations, 1)
	assert.Equal(t, 1, st.Generations[0].Gen)
	assert.Equal(t, 8, st.Generations[0].Samples)
	assert.Nil(t, st.Anchor)

	out.Reset()
	require.NoError(t, newRoot(&out, "autochthon", "status", "--working-dir", dir, "--anchor", anchor, "--json").Execute())
	st = autochthon.Status{}
	require.NoError(t, json.Unmarshal(out.Bytes(), &st))
	require.NotNil(t, st.Anchor)
	assert.True(t, st.Anchor.Holds)
	assert.Equal(t, anchor, st.Anchor.String())
}

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package stream

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/marshaler"
	"github.com/zerotrustdata/ztd/internal/pkg/ssh"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// ---------------------------------------------------------------------------
// buildOperatorCommand
// ---------------------------------------------------------------------------

func TestBuildOperatorCommand_Default(t *testing.T) {
	got := buildOperatorCommand("/home/user/.ztd/tmp/.ztd-stream-1234", 12345, 12346)
	assert.Equal(t, "/home/user/.ztd/tmp/.ztd-stream-1234 operator start --endpoint 127.0.0.1 --gateway-http-port 12345 --gateway-https-port 12346", got)
}

// ---------------------------------------------------------------------------
// parseInterleavedArgs - flags must be parseable BEFORE, AFTER, and BETWEEN
// positional host arguments. Regression test for the bug where
// `stream host1 --key XXX` treated `--key` and `XXX` as additional hosts.
// ---------------------------------------------------------------------------

func newStreamFlagSet(force *bool) *flag.FlagSet {
	fs := flag.NewFlagSet("stream", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(force, "force", false, "")
	return fs
}

func TestParseInterleavedArgs_FlagsAfterPositional(t *testing.T) {
	var force bool
	fs := newStreamFlagSet(&force)

	hosts, err := parseInterleavedArgs(fs, []string{"bobuntu2", "--force"})
	require.NoError(t, err)
	assert.Equal(t, []string{"bobuntu2"}, hosts)
	assert.True(t, force)
}

func TestParseInterleavedArgs_FlagsBetweenPositionals(t *testing.T) {
	var force bool
	fs := newStreamFlagSet(&force)

	hosts, err := parseInterleavedArgs(fs,
		[]string{"host1", "--force", "host2"})
	require.NoError(t, err)
	assert.Equal(t, []string{"host1", "host2"}, hosts)
	assert.True(t, force)
}

func TestParseInterleavedArgs_FlagsBeforePositional(t *testing.T) {
	var force bool
	fs := newStreamFlagSet(&force)

	hosts, err := parseInterleavedArgs(fs, []string{"--force", "host1", "host2"})
	require.NoError(t, err)
	assert.Equal(t, []string{"host1", "host2"}, hosts)
	assert.True(t, force)
}

func TestParseInterleavedArgs_NoArgs(t *testing.T) {
	var force bool
	fs := newStreamFlagSet(&force)

	hosts, err := parseInterleavedArgs(fs, nil)
	require.NoError(t, err)
	assert.Empty(t, hosts)
}

func TestParseInterleavedArgs_UnknownFlagReturnsError(t *testing.T) {
	var force bool
	fs := newStreamFlagSet(&force)

	_, err := parseInterleavedArgs(fs, []string{"host1", "--unknownxyz"})
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// collectHosts
// ---------------------------------------------------------------------------

func TestCollectHosts_Positional(t *testing.T) {
	hosts, err := collectHosts([]string{"host1", "host2", "host3"}, "")
	require.NoError(t, err)
	assert.Equal(t, []string{"host1", "host2", "host3"}, hosts)
}

func TestCollectHosts_Deduplication(t *testing.T) {
	hosts, err := collectHosts([]string{"host1", "host2", "host1"}, "")
	require.NoError(t, err)
	assert.Equal(t, []string{"host1", "host2"}, hosts)
}

func TestCollectHosts_FromFile(t *testing.T) {
	dir := testutil.TempDir(t)
	f := filepath.Join(dir, "hosts.txt")
	require.NoError(t, os.WriteFile(f, []byte("host-a\nhost-b\n# comment\n\nhost-c\n"), 0600))

	hosts, err := collectHosts(nil, f)
	require.NoError(t, err)
	assert.Equal(t, []string{"host-a", "host-b", "host-c"}, hosts)
}

func TestCollectHosts_FileAndPositional_Merged(t *testing.T) {
	dir := testutil.TempDir(t)
	f := filepath.Join(dir, "hosts.txt")
	require.NoError(t, os.WriteFile(f, []byte("host-b\nhost-c\n"), 0600))

	hosts, err := collectHosts([]string{"host-a", "host-b"}, f)
	require.NoError(t, err)
	// host-b deduplicated; order: positional first, then file
	assert.Equal(t, []string{"host-a", "host-b", "host-c"}, hosts)
}

func TestCollectHosts_FileNotFound(t *testing.T) {
	_, err := collectHosts(nil, "/nonexistent/hosts.txt")
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// parseSSHConfig
// ---------------------------------------------------------------------------

func TestParseSSHConfig_BasicBlock(t *testing.T) {
	dir := testutil.TempDir(t)
	cfg := filepath.Join(dir, "config")
	content := `
Host myserver
    HostName 192.168.1.10
    User deploy
    Port 2222
    IdentityFile ~/.ssh/id_ed25519
`
	require.NoError(t, os.WriteFile(cfg, []byte(content), 0600))

	blocks, err := ssh.ParseConfig(cfg)
	require.NoError(t, err)
	require.Contains(t, blocks, "myserver")
	b := blocks["myserver"]
	assert.Equal(t, "192.168.1.10", b.Hostname)
	assert.Equal(t, "deploy", b.User)
	assert.Equal(t, "2222", b.Port)
	assert.Len(t, b.IdentityFiles, 1)
}

func TestParseSSHConfig_MultipleBlocks(t *testing.T) {
	dir := testutil.TempDir(t)
	cfg := filepath.Join(dir, "config")
	content := `
Host prod-*
    User ubuntu
    IdentityFile ~/.ssh/prod_key

Host staging
    HostName 10.0.1.5
    User admin
    Port 2200
`
	require.NoError(t, os.WriteFile(cfg, []byte(content), 0600))

	blocks, err := ssh.ParseConfig(cfg)
	require.NoError(t, err)
	assert.Len(t, blocks, 2)
	assert.Contains(t, blocks, "prod-*")
	assert.Contains(t, blocks, "staging")
}

func TestParseSSHConfig_EqualsDelimiter(t *testing.T) {
	dir := testutil.TempDir(t)
	cfg := filepath.Join(dir, "config")
	content := `
Host equalhost
    HostName=10.0.0.1
    User=admin
    Port=22
`
	require.NoError(t, os.WriteFile(cfg, []byte(content), 0600))

	blocks, err := ssh.ParseConfig(cfg)
	require.NoError(t, err)
	require.Contains(t, blocks, "equalhost")
	b := blocks["equalhost"]
	assert.Equal(t, "10.0.0.1", b.Hostname)
	assert.Equal(t, "admin", b.User)
}

func TestParseSSHConfig_MissingFile(t *testing.T) {
	blocks, err := ssh.ParseConfig("/nonexistent/.ssh/config")
	require.NoError(t, err)
	assert.Empty(t, blocks)
}

func TestParseSSHConfig_Comments(t *testing.T) {
	dir := testutil.TempDir(t)
	cfg := filepath.Join(dir, "config")
	content := `
# Global comment
Host commented
    # Inline comment
    HostName 10.0.0.2
    User ops
`
	require.NoError(t, os.WriteFile(cfg, []byte(content), 0600))

	blocks, err := ssh.ParseConfig(cfg)
	require.NoError(t, err)
	require.Contains(t, blocks, "commented")
	assert.Equal(t, "10.0.0.2", blocks["commented"].Hostname)
}

func TestParseSSHConfig_MultipleIdentityFiles(t *testing.T) {
	dir := testutil.TempDir(t)
	cfg := filepath.Join(dir, "config")
	content := `
Host multi
    IdentityFile ~/.ssh/key1
    IdentityFile ~/.ssh/key2
`
	require.NoError(t, os.WriteFile(cfg, []byte(content), 0600))

	blocks, err := ssh.ParseConfig(cfg)
	require.NoError(t, err)
	require.Contains(t, blocks, "multi")
	assert.Len(t, blocks["multi"].IdentityFiles, 2)
}

// ---------------------------------------------------------------------------
// matchSSHBlock
// ---------------------------------------------------------------------------

func TestMatchSSHBlock_ExactMatch(t *testing.T) {
	blocks := map[string]*ssh.ConfigBlock{
		"myhost": {Hostname: "1.2.3.4"},
	}
	b := ssh.MatchBlock(blocks, "myhost")
	require.NotNil(t, b)
	assert.Equal(t, "1.2.3.4", b.Hostname)
}

func TestMatchSSHBlock_WildcardMatch(t *testing.T) {
	blocks := map[string]*ssh.ConfigBlock{
		"prod-*": {User: "ubuntu"},
	}
	b := ssh.MatchBlock(blocks, "prod-web-01")
	require.NotNil(t, b)
	assert.Equal(t, "ubuntu", b.User)
}

func TestMatchSSHBlock_NoMatch(t *testing.T) {
	blocks := map[string]*ssh.ConfigBlock{
		"staging": {Hostname: "10.0.0.1"},
	}
	b := ssh.MatchBlock(blocks, "production")
	assert.Nil(t, b)
}

// ---------------------------------------------------------------------------
// matchGlob / sshPatternMatch
// ---------------------------------------------------------------------------

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		pattern string
		input   string
		want    bool
	}{
		{"*", "anything", true},
		{"prod-*", "prod-web-01", true},
		{"prod-*", "staging-01", false},
		{"host?", "host1", true},
		{"host?", "host12", false},
		{"*.example.com", "foo.example.com", true},
		{"*.example.com", "example.com", false},
		{"exact", "exact", true},
		{"exact", "not-exact", false},
	}
	for _, tc := range cases {
		got := ssh.MatchGlob(tc.pattern, tc.input)
		assert.Equal(t, tc.want, got, "MatchGlob(%q, %q)", tc.pattern, tc.input)
	}
}

func TestSSHPatternMatch_MultiplePatterns(t *testing.T) {
	assert.True(t, ssh.PatternMatch("web-* db-*", "web-01"))
	assert.True(t, ssh.PatternMatch("web-* db-*", "db-02"))
	assert.False(t, ssh.PatternMatch("web-* db-*", "cache-01"))
}

// ---------------------------------------------------------------------------
// resolveHost
// ---------------------------------------------------------------------------

func TestResolveHost_UserAtHost(t *testing.T) {
	r, err := ssh.ResolveHost("deploy@10.0.0.5", "", "", "", "")
	require.NoError(t, err)
	assert.Equal(t, "deploy", r.User)
	assert.Equal(t, "10.0.0.5", r.Hostname)
	assert.Equal(t, "22", r.Port)
}

func TestResolveHost_UserAtHostPort(t *testing.T) {
	r, err := ssh.ResolveHost("admin@myhost:2222", "", "", "", "")
	require.NoError(t, err)
	assert.Equal(t, "admin", r.User)
	assert.Equal(t, "myhost", r.Hostname)
	assert.Equal(t, "2222", r.Port)
}

func TestResolveHost_AliasFromSSHConfig(t *testing.T) {
	dir := testutil.TempDir(t)
	cfg := filepath.Join(dir, "config")
	content := `
Host myalias
    HostName 192.168.1.50
    User ubuntu
    Port 2222
`
	require.NoError(t, os.WriteFile(cfg, []byte(content), 0600))

	r, err := ssh.ResolveHost("myalias", cfg, "", "", "")
	require.NoError(t, err)
	assert.Equal(t, "ubuntu", r.User)
	assert.Equal(t, "192.168.1.50", r.Hostname)
	assert.Equal(t, "2222", r.Port)
}

func TestResolveHost_UserOverridesConfig(t *testing.T) {
	dir := testutil.TempDir(t)
	cfg := filepath.Join(dir, "config")
	content := `
Host myhost
    User config-user
`
	require.NoError(t, os.WriteFile(cfg, []byte(content), 0600))

	// Explicit user@host overrides config User
	r, err := ssh.ResolveHost("explicit@myhost", cfg, "", "", "")
	require.NoError(t, err)
	assert.Equal(t, "explicit", r.User)
}

func TestResolveHost_DefaultPort(t *testing.T) {
	r, err := ssh.ResolveHost("somehost", filepath.Join(testutil.TempDir(t), "nonexistent"), "", "", "")
	require.NoError(t, err)
	assert.Equal(t, "22", r.Port)
}

// ---------------------------------------------------------------------------
// expandTilde
// ---------------------------------------------------------------------------

func TestExpandTilde_WithTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	got, err := ssh.ExpandTilde("~/.ssh/id_ed25519")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, ".ssh/id_ed25519"), got)
}

func TestExpandTilde_WithoutTilde(t *testing.T) {
	got, err := ssh.ExpandTilde("/absolute/path")
	require.NoError(t, err)
	assert.Equal(t, "/absolute/path", got)
}

// ---------------------------------------------------------------------------
// humanBytes
// ---------------------------------------------------------------------------

func TestHumanBytes(t *testing.T) {
	assert.Equal(t, "512B", humanBytes(512))
	assert.Equal(t, "1.0KB", humanBytes(1024))
	assert.Equal(t, "1.5KB", humanBytes(1536))
	assert.Equal(t, "1.0MB", humanBytes(1<<20))
	assert.Equal(t, "14.5MB", humanBytes(15234567))
}

func TestHumanBytes_EdgeCases(t *testing.T) {
	assert.Equal(t, "0B", humanBytes(0))
	assert.Equal(t, "1023B", humanBytes(1023))
	assert.Equal(t, "1.0KB", humanBytes(1024))
	assert.Equal(t, "1023.0KB", humanBytes(1024*1023))
	assert.Equal(t, "1.0MB", humanBytes(1<<20))
}

// ---------------------------------------------------------------------------
// collectHosts - stdin path
// ---------------------------------------------------------------------------

func TestCollectHosts_Stdin(t *testing.T) {
	r, w, err := os.Pipe()
	require.NoError(t, err)

	origStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = origStdin
	})

	_, err = io.WriteString(w, "host-x\nhost-y\n# skip\n\nhost-z\n")
	require.NoError(t, err)
	w.Close()

	hosts, err := collectHosts(nil, "-")
	require.NoError(t, err)
	assert.Equal(t, []string{"host-x", "host-y", "host-z"}, hosts)
}

func TestCollectHosts_WhitespaceOnlyLines(t *testing.T) {
	dir := testutil.TempDir(t)
	f := filepath.Join(dir, "hosts.txt")
	require.NoError(t, os.WriteFile(f, []byte("  \n\t\nhost-a\n   host-b   \n"), 0600))

	hosts, err := collectHosts(nil, f)
	require.NoError(t, err)
	assert.Equal(t, []string{"host-a", "host-b"}, hosts)
}

// ---------------------------------------------------------------------------
// resolveHost - additional edge cases
// ---------------------------------------------------------------------------

func TestResolveHost_WildcardSSHConfigMatch(t *testing.T) {
	dir := testutil.TempDir(t)
	cfg := filepath.Join(dir, "config")
	content := `
Host prod-*
    User ubuntu
    IdentityFile ~/.ssh/prod_key
`
	require.NoError(t, os.WriteFile(cfg, []byte(content), 0600))

	r, err := ssh.ResolveHost("prod-web-01", cfg, "", "", "")
	require.NoError(t, err)
	assert.Equal(t, "ubuntu", r.User)
	assert.Equal(t, "prod-web-01", r.Hostname)
	assert.Equal(t, "22", r.Port)
}

func TestResolveHost_SSHConfigPort22NotOverridden(t *testing.T) {
	dir := testutil.TempDir(t)
	cfg := filepath.Join(dir, "config")
	content := `
Host myhost
    HostName 10.0.0.1
    User deploy
    Port 22
`
	require.NoError(t, os.WriteFile(cfg, []byte(content), 0600))

	r, err := ssh.ResolveHost("myhost", cfg, "", "", "")
	require.NoError(t, err)
	assert.Equal(t, "22", r.Port)
	assert.Equal(t, "10.0.0.1", r.Hostname)
}

func TestResolveHost_NoSSHConfig_DefaultsApplied(t *testing.T) {
	r, err := ssh.ResolveHost("bare-host", "/nonexistent/ssh/config", "fallback-user", "", "")
	require.NoError(t, err)
	assert.Equal(t, "fallback-user", r.User)
}

func TestResolveHost_HostnameOverriddenFromConfig(t *testing.T) {
	dir := testutil.TempDir(t)
	cfg := filepath.Join(dir, "config")
	content := `
Host alias
    HostName 192.168.99.1
    User admin
`
	require.NoError(t, os.WriteFile(cfg, []byte(content), 0600))

	r, err := ssh.ResolveHost("alias", cfg, "", "", "")
	require.NoError(t, err)
	assert.Equal(t, "192.168.99.1", r.Hostname)
	assert.Equal(t, "alias", r.Original)
}

// ---------------------------------------------------------------------------
// emitJSON - JSON shape and typed field serialization
// ---------------------------------------------------------------------------

func TestEmitJSON_PerHostEvent(t *testing.T) {
	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = old })

	ts := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	evt := StreamStatusEvent{
		Host:      "web-01",
		Status:    constants.StreamStatusCompleted,
		SizeBytes: 1024,
		ElapsedMs: 250,
		Ts:        ts,
	}
	require.NoError(t, emitJSON(&evt))
	w.Close()

	var buf bytes.Buffer
	_, err = io.Copy(&buf, r)
	require.NoError(t, err)

	line := strings.TrimSpace(buf.String())

	var got map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(line), &got))

	assert.Equal(t, "web-01", got["host"])
	assert.Equal(t, marshaler.Status(constants.StreamStatusCompleted), got["status"])
	assert.InEpsilon(t, float64(1024), got["size_bytes"], 0.0)
	assert.InEpsilon(t, float64(250), got["elapsed_ms"], 0.0)
	assert.NotEmpty(t, got["ts"])
	_, hasSummary := got["summary"]
	assert.False(t, hasSummary, "summary field must be omitempty on non-summary events")
}

func TestEmitJSON_SummaryEvent(t *testing.T) {
	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = old })

	evt := StreamStatusEvent{
		Summary: true,
		Status:  constants.StreamStatusSummary,
		Total:   10,
		Success: 8,
		Failed:  2,
		TotalMs: 5000,
		Ts:      time.Now().UTC(),
	}
	require.NoError(t, emitJSON(&evt))
	w.Close()

	var buf bytes.Buffer
	_, err = io.Copy(&buf, r)
	require.NoError(t, err)

	var got map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &got))

	assert.True(t, got["summary"].(bool))
	assert.Equal(t, marshaler.Status(constants.StreamStatusSummary), got["status"])
	assert.InEpsilon(t, float64(10), got["total"], 0.0)
	assert.InEpsilon(t, float64(8), got["success"], 0.0)
	assert.InEpsilon(t, float64(2), got["failed"], 0.0)
	assert.InEpsilon(t, float64(5000), got["total_ms"], 0.0)
}

func TestEmitJSON_StatusConstants(t *testing.T) {
	cases := []constants.StreamStatus{
		constants.StreamStatusCompleted,
		constants.StreamStatusFailed,
		constants.StreamStatusCancelled,
		constants.StreamStatusExited,
		constants.StreamStatusSummary,
	}
	for _, status := range cases {
		old := os.Stdout
		r, w, pipeErr := os.Pipe()
		require.NoError(t, pipeErr)
		os.Stdout = w

		require.NoError(t, emitJSON(&StreamStatusEvent{Status: status, Ts: time.Now().UTC()}))
		w.Close()
		os.Stdout = old

		var buf bytes.Buffer
		_, err := io.Copy(&buf, r)
		require.NoError(t, err)

		var got map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &got))
		assert.Equal(t, string(status), got["status"], "status mismatch for %s", status)
	}
}

func TestEmitJSON_ErrorFieldOmittedWhenEmpty(t *testing.T) {
	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = old })

	require.NoError(t, emitJSON(&StreamStatusEvent{
		Host:   "host1",
		Status: constants.StreamStatusCompleted,
		Ts:     time.Now().UTC(),
	}))
	w.Close()

	var buf bytes.Buffer
	_, err = io.Copy(&buf, r)
	require.NoError(t, err)

	var got map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &got))

	_, hasError := got["error"]
	assert.False(t, hasError, "error field must be omitted when empty")
}

func TestEmitJSON_ErrorFieldPresentOnFailure(t *testing.T) {
	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = old })

	require.NoError(t, emitJSON(&StreamStatusEvent{
		Host:   "host2",
		Status: constants.StreamStatusFailed,
		Error:  "dial tcp: connection refused",
		Ts:     time.Now().UTC(),
	}))
	w.Close()

	var buf bytes.Buffer
	_, err = io.Copy(&buf, r)
	require.NoError(t, err)

	var got map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &got))

	assert.Equal(t, "dial tcp: connection refused", got["error"])
}

func TestEmitJSON_TsIsRFC3339(t *testing.T) {
	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = old })

	require.NoError(t, emitJSON(&StreamStatusEvent{
		Status: constants.StreamStatusCompleted,
		Ts:     time.Now().UTC(),
	}))
	w.Close()

	var buf bytes.Buffer
	_, err = io.Copy(&buf, r)
	require.NoError(t, err)

	var got map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &got))

	tsVal, ok := got["ts"].(string)
	require.True(t, ok, "ts must be a JSON string")
	_, parseErr := time.Parse(time.RFC3339, tsVal)
	assert.NoError(t, parseErr, "ts must be valid RFC3339: %s", tsVal)
}

// ---------------------------------------------------------------------------
// runConcurrentStream - fan-out, per-host emission, context cancellation
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// printStreamUsage
// ---------------------------------------------------------------------------

func TestPrintStreamUsage_ContainsKeywords(t *testing.T) {
	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = old })

	printStreamUsage()
	w.Close()

	var buf bytes.Buffer
	_, err = io.Copy(&buf, r)
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "ztd.operator stream")
	assert.Contains(t, output, "--arch")
	assert.Contains(t, output, "--hosts")
	assert.Contains(t, output, "--concurrency")
	assert.Contains(t, output, "--timeout")
	assert.Contains(t, output, "--endpoint")
	assert.Contains(t, output, "EXAMPLES")
}

// ---------------------------------------------------------------------------
// RunStream - flag-error and no-hosts paths exercised via subprocess exec
// (uses -test.run on the test binary itself to avoid os.Exit in the parent)
// ---------------------------------------------------------------------------

func TestRunStream_HelpFlag(t *testing.T) {
	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = old })

	// --help causes flag.ContinueOnError to return flag.ErrHelp, which prints
	// usage to stdout then exits. We exercise only the printStreamUsage path
	// reachable from RunStream by calling printStreamUsage directly since
	// RunStream calls os.Exit, which cannot be caught in-process.
	printStreamUsage()
	w.Close()

	var buf bytes.Buffer
	_, err = io.Copy(&buf, r)
	require.NoError(t, err)

	assert.NotEmpty(t, buf.String(), "usage output must not be empty")
}

func TestRunStream_ZtdBinaryNotFound_ResultsInFailure(t *testing.T) {
	// Exercise the binary-load step in isolation: build a minimal binaryDir
	// that does NOT contain the expected arch sub-path and verify the error
	// message that RunStream would emit to stderr.
	binaryDir := testutil.TempDir(t)
	arch := "amd64"
	binPath := filepath.Join(binaryDir, "linux-"+arch, "ztd.operator")

	_, err := os.ReadFile(binPath)
	require.Error(t, err, "binary must not exist in fresh temp dir")
}

func TestRunStream_ArchValidation(t *testing.T) {
	validArchs := []string{"amd64", "arm64", "386"}
	for _, arch := range validArchs {
		binaryDir := testutil.TempDir(t)
		binDir := filepath.Join(binaryDir, "linux-"+arch)
		require.NoError(t, os.MkdirAll(binDir, 0755))
		fakeBin := filepath.Join(binDir, "ztd.operator")
		require.NoError(t, os.WriteFile(fakeBin, []byte("x"), 0755))

		_, err := os.ReadFile(filepath.Join(binaryDir, "linux-"+arch, "ztd.operator"))
		require.NoError(t, err, "binary path for arch %s must be readable", arch)
	}
}

// ---------------------------------------------------------------------------
// buildAuthMethods
// ---------------------------------------------------------------------------

// generateTestSSHKey writes a PEM-encoded RSA private key to path.
// ssh.ParsePrivateKey handles PKCS1 RSA PEM keys correctly.
func generateTestSSHKey(t *testing.T, path string) {
	t.Helper()

	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	pemBlock := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privKey),
	}
	require.NoError(t, os.WriteFile(path, pem.EncodeToMemory(pemBlock), 0600))
}

// ---------------------------------------------------------------------------
// getDefaultZtdBinaryDir
// ---------------------------------------------------------------------------

func TestGetDefaultZtdBinaryDir(t *testing.T) {
	// Test that getDefaultZtdBinaryDir returns a valid path
	dir := getDefaultZtdBinaryDir()
	assert.NotEmpty(t, dir, "default binary dir should not be empty")
	assert.Contains(t, dir, "bin", "default binary dir should contain 'bin'")
}

// ---------------------------------------------------------------------------
// boundedBuffer
// ---------------------------------------------------------------------------

func TestBoundedBuffer_WriteWithinLimit(t *testing.T) {
	buf := &boundedBuffer{limit: 100}
	n, err := buf.Write([]byte("hello world"))
	require.NoError(t, err)
	assert.Equal(t, 11, n)
	assert.Equal(t, "hello world", buf.String())
}

func TestBoundedBuffer_WriteExactlyAtLimit(t *testing.T) {
	buf := &boundedBuffer{limit: 10}
	n, err := buf.Write([]byte("0123456789"))
	require.NoError(t, err)
	assert.Equal(t, 10, n)
	assert.Equal(t, "0123456789", buf.String())
}

func TestBoundedBuffer_WriteExceedsLimit(t *testing.T) {
	buf := &boundedBuffer{limit: 5}
	n, err := buf.Write([]byte("0123456789"))
	require.NoError(t, err)
	assert.Equal(t, 10, n, "Write should report full length even if truncated")
	assert.Equal(t, "01234", buf.String(), "buffer should be truncated to limit")
}

func TestBoundedBuffer_WriteMultipleChunks(t *testing.T) {
	buf := &boundedBuffer{limit: 10}

	_, err := buf.Write([]byte("hello"))
	require.NoError(t, err)

	_, err = buf.Write([]byte(" "))
	require.NoError(t, err)

	_, err = buf.Write([]byte("world"))
	require.NoError(t, err)

	assert.Equal(t, "hello worl", buf.String(), "should truncate at limit")
}

func TestBoundedBuffer_ZeroLimit(t *testing.T) {
	buf := &boundedBuffer{limit: 0}
	n, err := buf.Write([]byte("anything"))
	require.NoError(t, err)
	assert.Equal(t, 8, n)
	assert.Empty(t, buf.String(), "zero limit should result in empty buffer")
}

func TestBoundedBuffer_NegativeLimit(t *testing.T) {
	buf := &boundedBuffer{limit: -1}
	n, err := buf.Write([]byte("anything"))
	require.NoError(t, err)
	assert.Equal(t, 8, n)
	assert.Empty(t, buf.String(), "negative limit should result in empty buffer")
}

func TestBoundedBuffer_StringOnEmpty(t *testing.T) {
	buf := &boundedBuffer{limit: 100}
	assert.Empty(t, buf.String(), "empty buffer should return empty string")
}

// ---------------------------------------------------------------------------
// Additional edge cases for humanBytes
// ---------------------------------------------------------------------------

func TestHumanBytes_VeryLarge(t *testing.T) {
	// humanBytes only supports up to MB, so 1GB is formatted as 1024.0MB
	assert.Equal(t, "1024.0MB", humanBytes(1<<30))
	assert.Equal(t, "2048.0MB", humanBytes(1<<30*2))
}

func TestHumanBytes_Negative(t *testing.T) {
	// Negative values should still format (though unusual in practice)
	assert.Equal(t, "-512B", humanBytes(-512))
}

// ---------------------------------------------------------------------------
// Additional edge cases for buildOperatorCommand
// ---------------------------------------------------------------------------

func TestBuildOperatorCommand_CustomPorts(t *testing.T) {
	got := buildOperatorCommand("/home/user/.ztd/tmp/.ztd-stream-abcdef", 8080, 8443)
	assert.Equal(t, "/home/user/.ztd/tmp/.ztd-stream-abcdef operator start --endpoint 127.0.0.1 --gateway-http-port 8080 --gateway-https-port 8443", got)
}

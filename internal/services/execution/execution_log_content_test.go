// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package execution

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// debugLogger returns a logger that records every level, so a leak at any level
// is visible to the assertions.
func debugLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})), &buf
}

// TestStreamingWriter_DoesNotLogOutput proves captured stdout/stderr is kept in
// the buffer only. Command output is execution content and must never reach the
// plaintext log (INV-STOR-ENC-01).
func TestStreamingWriter_DoesNotLogOutput(t *testing.T) {
	t.Parallel()
	logger, logs := debugLogger()
	var captured bytes.Buffer
	sw := newStreamingWriter(&captured, logger, "stdout", "exec-1", 0)

	input := "AWS_SECRET_ACCESS_KEY=hunter2\npartial line without newline"
	n, err := sw.Write([]byte(input))
	require.NoError(t, err)
	assert.Equal(t, len(input), n)

	assert.Equal(t, input, captured.String(), "the buffer keeps the raw output for the encrypted event")
	assert.Empty(t, logs.String(), "output lines must not be logged")
}

// TestStreamingWriter_TruncationLogsMetadataOnly proves the truncation warning
// carries the execution ID, stream and limit, and none of the output.
func TestStreamingWriter_TruncationLogsMetadataOnly(t *testing.T) {
	t.Parallel()
	logger, logs := debugLogger()
	var captured bytes.Buffer
	sw := newStreamingWriter(&captured, logger, "stderr", "exec-2", 8)

	n, err := sw.Write([]byte("top-secret-token-value\n"))
	require.NoError(t, err)
	assert.Equal(t, len("top-secret-token-value\n"), n, "a capped write still reports every byte consumed")

	_, err = sw.Write([]byte("more-secret-output\n"))
	require.NoError(t, err)

	out := logs.String()
	assert.Contains(t, out, "exec-2")
	assert.Contains(t, out, "stderr")
	assert.Equal(t, 1, strings.Count(out, "limit reached"), "truncation is reported once")
	assert.NotContains(t, out, "secret")
}

// TestExecuteCommand_LogsNoExecutionContent runs a real command at debug level
// and asserts neither the command line, its arguments, nor its output reach the log.
func TestExecuteCommand_LogsNoExecutionContent(t *testing.T) {
	t.Parallel()
	logger, logs := debugLogger()
	svc := NewExecutionService(testutil.NewTestConfig(t), logger)

	workDir := testutil.TempDir(t)
	result, err := svc.ExecuteCommand(context.Background(), &models.ExecutionRequestPayload{
		ExecutionID:      "exec-3",
		CaseID:           "case-3",
		Command:          "echo",
		Args:             []string{"ARG_SENTINEL_9a1f", "stdout-sentinel-4b7c"},
		WorkingDirectory: &workDir,
		TimeoutSeconds:   5,
		RequestedBy:      "test-user",
	})
	require.NoError(t, err)
	require.Contains(t, result.Stdout, "stdout-sentinel-4b7c")

	out := logs.String()
	assert.Contains(t, out, "exec-3", "the execution ID is the correlation handle")
	for _, content := range []string{"ARG_SENTINEL_9a1f", "stdout-sentinel-4b7c", "echo", workDir} {
		assert.NotContains(t, out, content, "execution content leaked into the log")
	}
}

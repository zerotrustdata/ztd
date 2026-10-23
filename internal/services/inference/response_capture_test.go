// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package inference

import (
	"context"
	"io"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/testutil"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestProviderResponseCapture_IngestsRawTailWhileProgressDeliveryIsBlocked(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		reader, writer := io.Pipe()
		defer reader.Close()
		defer writer.Close()
		// "hello" is released once the next line is buffered as lookahead, and the reporter blocks on it.
		first := `{"model":"test-model","message":{"content":"hello\nworld\n"}}` + "\n"
		tail := `{"model":"test-model","done":true,"done_reason":"stop"}` + "\n"
		capture := newProviderResponseCapture(defaultMaxResponseBytes, time.Now())
		go capture.receive(reader)
		backend, err := NewOllamaBackend("http://localhost:11434", testutil.NewTestLogger())
		require.NoError(t, err)
		reporting := make(chan struct{})
		release := make(chan struct{})
		decoded := make(chan error, 1)
		closeReporting := sync.OnceFunc(func() { close(reporting) })
		stream, err := newStreamSegmenter(mustNewScrubbingSvc(t), func(*operatorv1.InferenceProgressEvent) error {
			closeReporting()
			<-release
			return nil
		}, "attempt-blocked")
		require.NoError(t, err)
		go func() {
			_, _, _, err := backend.decodeChatStream(context.Background(), capture, capture.arrivalTime, true, nil, "attempt-blocked", stream)
			decoded <- err
		}()
		written := make(chan error, 1)
		go func() {
			_, err := io.WriteString(writer, first)
			if err == nil {
				<-reporting
				_, err = io.WriteString(writer, tail)
			}
			_ = writer.Close()
			written <- err
		}()
		<-reporting
		synctest.Wait()
		select {
		case <-capture.finished:
			raw, err := capture.result()
			require.NoError(t, err)
			assert.Equal(t, first+tail, string(raw))
		default:
			t.Error("progress delivery blocked provider ingestion")
		}
		close(release)
		assert.NoError(t, <-written)
		assert.NoError(t, <-decoded)
	})
}

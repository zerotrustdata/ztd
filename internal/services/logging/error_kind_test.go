// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package logging

import (
	"fmt"
	"io"
	"net"
	"net/url"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestIsTransportDown_IgnoresRenderedTransportWords(t *testing.T) {
	assert.True(t, IsTransportDown(fmt.Errorf("read tcp 127.0.0.1:1->127.0.0.1:8443: %w", syscall.ECONNRESET)))
	assert.True(t, IsTransportDown(fmt.Errorf("dial: %w", syscall.ECONNREFUSED)))
	assert.True(t, IsTransportDown(fmt.Errorf("failed to execute HTTP request: %w", io.EOF)))
	assert.True(t, IsTransportDown(fmt.Errorf("response: %w", io.ErrUnexpectedEOF)))
	assert.False(t, IsTransportDown(fmt.Errorf("public-feed: mirror rejected proof package")))
	assert.False(t, IsTransportDown(fmt.Errorf("proof package EOF marker in digest: %w", constants.ErrInternal)))
}

func TestErrorKind_ClassifiesWrappedProviderFailuresWithoutContent(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want string
	}{
		{"refused", syscall.ECONNREFUSED, "connection_refused"},
		{"reset", syscall.ECONNRESET, "connection_reset"},
		{"broken pipe", syscall.EPIPE, "broken_pipe"},
		{"EOF", io.EOF, "eof"},
		{"truncated response", io.ErrUnexpectedEOF, "unexpected_eof"},
		{"DNS", &net.DNSError{Err: "private hostname", Name: "private.example"}, "dns"},
		{"empty file content", constants.ErrFileEditContentRequired, "invalid_input"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := &url.Error{Op: "Post", URL: "http://private.example/private", Err: fmt.Errorf("private data: %w", test.err)}
			assert.Equal(t, test.want, ErrorKind(err).Value.String())
			assert.Equal(t, test.want, ErrorKindValue(err))
		})
	}
}

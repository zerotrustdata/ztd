// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package logging

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"syscall"

	"github.com/zerotrustdata/ztd/internal/constants"
)

const (
	ComponentKey              = "component"
	ComponentGateway          = "gateway"
	ComponentEmbeddedOperator = "embedded-operator"
	ComponentOperator         = "operator"
)

// NewLogger returns a slog.Logger writing to the given writer with ztd
// log formatting. Used by LogService.ConfigureFileLogger and by CLI
// clients that log to stdout.
func NewLogger(level string, output io.Writer) (*slog.Logger, error) {
	parsedLevel, err := parseLogLevel(level)
	if err != nil {
		return nil, fmt.Errorf("logging: parse level: %w", err)
	}
	return slog.New(newLogHandler(output, parsedLevel)), nil
}

// NewStdoutLogger returns a slog.Logger writing to stdout. Used by CLI
// client commands (ztd operator, ztd auth, etc.) that do not run as
// daemons and do not write to .ztd/logs/ztd.log.
func NewStdoutLogger(level string) (*slog.Logger, error) {
	return NewLogger(level, os.Stdout)
}

// ErrorKindKey is the attribute key ErrorKind emits.
const ErrorKindKey = "error_kind"

// Error kinds ErrorKind reports. They are a closed set so a log line carries a
// category and never the error text.
const (
	errorKindNotFound      = "not_found"
	errorKindPermission    = "permission"
	errorKindExists        = "exists"
	errorKindTimeout       = "timeout"
	errorKindCanceled      = "canceled"
	errorKindOther         = "other"
	errorKindRefused       = "connection_refused"
	errorKindReset         = "connection_reset"
	errorKindBrokenPipe    = "broken_pipe"
	errorKindEOF           = "eof"
	errorKindUnexpectedEOF = "unexpected_eof"
	errorKindDNS           = "dns"
	errorKindInvalidInput  = "invalid_input"
)

// ErrorKind returns a log attribute that classifies err without its text.
// Errors from execution paths (exec, os, filesystem) embed the command line or
// the requested path, which is execution content and must not reach the
// plaintext log (INV-STOR-ENC-01). Log ErrorKind(err) in place of "error", err
// on those paths; the full error belongs in the encrypted execution record.
func ErrorKind(err error) slog.Attr {
	return slog.String(ErrorKindKey, ErrorKindValue(err))
}

// ErrorKindValue returns the classification string for err without its text.
// Use with ErrorKindKey in key-value logger calls:
//
//	logger.Warn("Failed", "execution_id", id, logging.ErrorKindKey, logging.ErrorKindValue(err))
func ErrorKindValue(err error) string {
	kind := errorKindOther
	var dnsErr *net.DNSError
	var netErr net.Error
	switch {
	case err == nil:
		kind = "none"
	case errors.Is(err, fs.ErrNotExist):
		kind = errorKindNotFound
	case errors.Is(err, fs.ErrPermission):
		kind = errorKindPermission
	case errors.Is(err, fs.ErrExist):
		kind = errorKindExists
	case errors.Is(err, context.DeadlineExceeded):
		kind = errorKindTimeout
	case errors.Is(err, context.Canceled):
		kind = errorKindCanceled
	case errors.Is(err, syscall.ECONNREFUSED) || isPlatformConnectionRefused(err):
		kind = errorKindRefused
	case errors.Is(err, syscall.ECONNRESET) || isPlatformConnectionReset(err):
		kind = errorKindReset
	case errors.Is(err, syscall.EPIPE):
		kind = errorKindBrokenPipe
	case errors.Is(err, io.ErrUnexpectedEOF):
		kind = errorKindUnexpectedEOF
	case errors.Is(err, io.EOF):
		kind = errorKindEOF
	case errors.As(err, &netErr) && netErr.Timeout():
		kind = errorKindTimeout
	case errors.As(err, &dnsErr):
		kind = errorKindDNS
	case errors.Is(err, constants.ErrFileEditContentRequired),
		errors.Is(err, constants.ErrFileEditOldContentRequired),
		errors.Is(err, constants.ErrFileEditInsertContentRequired),
		errors.Is(err, constants.ErrFileEditLineRangeRequired),
		errors.Is(err, constants.ErrFileEditPatchContentRequired):
		kind = errorKindInvalidInput
	}
	return kind
}

// IsClientDisconnect reports whether err is a client-initiated disconnect or
// cancellation on a response write (broken pipe, connection reset/abort,
// closed network connection, or context cancellation).
func IsClientDisconnect(err error) bool {
	return errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ECONNRESET) ||
		isPlatformConnectionReset(err) ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, context.Canceled)
}

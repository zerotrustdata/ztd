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
	"os"
)

const (
	ComponentKey              = "component"
	ComponentGateway          = "gateway"
	ComponentEmbeddedOperator = "embedded-operator"
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
	errorKindNotFound   = "not_found"
	errorKindPermission = "permission"
	errorKindExists     = "exists"
	errorKindTimeout    = "timeout"
	errorKindCanceled   = "canceled"
	errorKindOther      = "other"
)

// ErrorKind returns a log attribute that classifies err without its text.
// Errors from execution paths (exec, os, filesystem) embed the command line or
// the requested path, which is execution content and must not reach the
// plaintext log (INV-STOR-ENC-01). Log ErrorKind(err) in place of "error", err
// on those paths; the full error belongs in the encrypted execution record.
func ErrorKind(err error) slog.Attr {
	kind := errorKindOther
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
	}
	return slog.String(ErrorKindKey, kind)
}

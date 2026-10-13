// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build !windows

package netutil

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"runtime"
	"strconv"
	"syscall"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func reserveTCPPort() (int, io.Closer, error) {
	listener, err := net.Listen(string(constants.NetworkProtocolTCP), net.JoinHostPort(constants.LocalhostIP, "0"))
	if err != nil {
		return 0, nil, err
	}
	return listener.Addr().(*net.TCPAddr).Port, listener, nil
}

func checkTCPPortBind(port int) error {
	// Go enables SO_REUSEADDR for TCP listeners. On Linux that matches the real
	// Gateway bind exactly: any listening socket on the port, wildcard or
	// specific, still conflicts, while TIME_WAIT sockets left by a Gateway that
	// just stopped do not. Disabling it there would report a freshly stopped
	// Gateway's ports as busy for a minute and shift the restart off the
	// defaults. On BSD, SO_REUSEADDR lets a wildcard bind coexist with a
	// listener on a specific address, so the probe disables it there.
	var config net.ListenConfig
	if runtime.GOOS != "linux" {
		config.Control = func(_, _ string, conn syscall.RawConn) error {
			var optionErr error
			controlErr := conn.Control(func(fd uintptr) {
				optionErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 0)
			})
			return errors.Join(controlErr, optionErr)
		}
	}
	// Probe each family explicitly so IPv4 and IPv6 conflicts are both detected.
	for _, network := range []string{"tcp4", "tcp6"} {
		listener, err := config.Listen(context.Background(), network, net.JoinHostPort("", strconv.Itoa(port)))
		if network == "tcp6" && (errors.Is(err, syscall.EAFNOSUPPORT) || errors.Is(err, syscall.EPROTONOSUPPORT) || errors.Is(err, syscall.EADDRNOTAVAIL)) {
			continue // IPv4-only hosts do not need an IPv6 reservation.
		}
		if err != nil {
			return fmt.Errorf("bind %s probe: %w", network, err)
		}
		if err := listener.Close(); err != nil {
			return fmt.Errorf("close %s probe: %w", network, err)
		}
	}
	return nil
}

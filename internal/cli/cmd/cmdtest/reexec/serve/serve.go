// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

// Package serve contains the listener-capable re-exec mode used only by
// integration tests.
package serve

import (
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// IfRequested starts the minimal health server when an integration test binary
// is re-executed as a Gateway child in serve mode.
func IfRequested() {
	if len(os.Args) <= 1 || os.Args[1] != "gw" || os.Getenv(string(constants.EnvVar.TestReexec)) != "serve" {
		return
	}
	port := parseHTTPPortFromArgs(os.Args)
	if port == 0 {
		os.Exit(0)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"status":"ok","mode":"gateway","posture":"doctrine","pid":%d}`, os.Getpid())
	})
	srv := &http.Server{
		Addr:              "127.0.0.1:" + strconv.Itoa(port),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "re-exec health server: %v\n", err)
		os.Exit(1)
	}
}

func parseHTTPPortFromArgs(args []string) int {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--http-port" {
			port, err := strconv.Atoi(args[i+1])
			if err != nil {
				return 0
			}
			return port
		}
	}
	return 0
}

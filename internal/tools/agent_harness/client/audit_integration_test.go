// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/config"
)

func TestDiscoverOperatorKeepsIdentityPaired(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"success":true,"operators":[{"id":"embedded-operator","operator_session_id":"embedded-session","status":"active","operator_type":"embedded"},{"id":"data-worker","operator_session_id":"data-session","status":"active","operator_type":"remote","operator_capabilities":["data"]}]}`))
	}))
	t.Cleanup(server.Close)
	for _, tt := range []struct{ name, id, session, wantID, wantSession string }{
		{"select data worker", "", "", "data-worker", "data-session"},
		{"resolve logical ID", "data-worker", "", "data-worker", "data-session"},
		{"resolve session", "", "data-session", "data-worker", "data-session"},
		{"reject conflicting pair", "embedded-operator", "data-session", "", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, err := New(config.Config{MTLSBaseURL: server.URL, UserID: "user-123", OperatorID: tt.id, OperatorSessionID: tt.session})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			id, session, discoverErr := c.DiscoverOperator(ctx)
			if tt.wantID == "" {
				if discoverErr == nil {
					t.Fatal("expected discovery rejection")
				}
			} else if discoverErr != nil {
				t.Fatal(discoverErr)
			}
			if id != tt.wantID || session != tt.wantSession {
				t.Fatalf("got (%q, %q), want (%q, %q)", id, session, tt.wantID, tt.wantSession)
			}
		})
	}
}

func TestDiscoverOperatorRejectsUnresolvedTargets(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		status     int
		want       error
	}{
		{"missing target", `{"success":true,"operators":[]}`, http.StatusOK, constants.ErrEvaluationTargetUnavailable},
		{"registry unauthorized", `{}`, http.StatusUnauthorized, constants.ErrHTTPStatusError},
		{"malformed registry", `broken`, http.StatusOK, constants.ErrInvalidJSONResponse},
		{"incomplete registry", `{"success":false}`, http.StatusOK, constants.ErrInvalidJSONResponse},
		{"ambiguous data workers", `{"success":true,"operators":[{"id":"one","operator_session_id":"s1","status":"active","operator_type":"remote","operator_capabilities":["data"]},{"id":"two","operator_session_id":"s2","status":"active","operator_type":"remote","operator_capabilities":["data"]}]}`, http.StatusOK, constants.ErrEvaluationTargetAmbiguous},
		{"inactive worker", `{"success":true,"operators":[{"id":"one","operator_session_id":"s1","status":"inactive","operator_type":"remote","operator_capabilities":["data"]}]}`, http.StatusOK, constants.ErrEvaluationTargetUnavailable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tt.status); w.Write([]byte(tt.body)) }))
			t.Cleanup(srv.Close)
			c, err := New(config.Config{MTLSBaseURL: srv.URL, UserID: "user-123"})
			if err != nil {
				t.Fatal(err)
			}
			id, session, err := c.DiscoverOperator(t.Context())
			if id != "" || session != "" || !errors.Is(err, tt.want) {
				t.Fatalf("got (%q,%q,%v), want %v", id, session, err, tt.want)
			}
		})
	}
}

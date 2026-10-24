// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package mcptransport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestServeStdio_BasicRequestResponse(t *testing.T) {
	input := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n"
	in := strings.NewReader(input)
	var out bytes.Buffer

	handler := HandlerFunc(func(ctx context.Context, req JSONRPCRequest) (JSONRPCResponse, error) {
		assert.Equal(t, "tools/list", req.Method)
		return JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  map[string]interface{}{"tools": []interface{}{}},
		}, nil
	})

	err := ServeStdio(context.Background(), in, &out, slog.Default(), handler)
	require.NoError(t, err)

	var resp JSONRPCResponse
	err = json.Unmarshal(out.Bytes(), &resp)
	require.NoError(t, err)
	assert.Equal(t, "2.0", resp.JSONRPC)
	assert.InDelta(t, float64(1), resp.ID, 0)
	assert.Nil(t, resp.Error)
	assert.NotNil(t, resp.Result)
}

func TestServeStdio_NotificationDropped(t *testing.T) {
	// Notification has method but no id
	input := `{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"ping"}` + "\n"
	in := strings.NewReader(input)
	var out bytes.Buffer

	handledMethods := []string{}
	handler := HandlerFunc(func(ctx context.Context, req JSONRPCRequest) (JSONRPCResponse, error) {
		handledMethods = append(handledMethods, req.Method)
		return JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  "pong",
		}, nil
	})

	err := ServeStdio(context.Background(), in, &out, slog.Default(), handler)
	require.NoError(t, err)

	// Notification should not reach the handler or produce output
	assert.Equal(t, []string{"ping"}, handledMethods)

	var resp JSONRPCResponse
	err = json.Unmarshal(out.Bytes(), &resp)
	require.NoError(t, err)
	assert.InDelta(t, float64(2), resp.ID, 0)
	assert.Equal(t, "pong", resp.Result)
}

func TestServeStdio_ParseError(t *testing.T) {
	input := "invalid-json\n" +
		`{"jsonrpc":"2.0","id":3,"method":"ping"}` + "\n"
	in := strings.NewReader(input)
	var out bytes.Buffer

	handler := HandlerFunc(func(ctx context.Context, req JSONRPCRequest) (JSONRPCResponse, error) {
		return JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  "pong",
		}, nil
	})

	err := ServeStdio(context.Background(), in, &out, slog.Default(), handler)
	require.NoError(t, err)

	decoder := json.NewDecoder(&out)

	// First response should be parse error
	var errResp JSONRPCResponse
	require.NoError(t, decoder.Decode(&errResp))
	assert.Equal(t, "2.0", errResp.JSONRPC)
	require.NotNil(t, errResp.Error)
	assert.Equal(t, constants.JSONRPCErrorCodeParseError, errResp.Error.Code)

	// Second response should be valid ping response
	var okResp JSONRPCResponse
	require.NoError(t, decoder.Decode(&okResp))
	assert.InDelta(t, float64(3), okResp.ID, 0)
	assert.Equal(t, "pong", okResp.Result)
}

func TestServeStdio_HandlerError(t *testing.T) {
	input := `{"jsonrpc":"2.0","id":4,"method":"fail"}` + "\n"
	in := strings.NewReader(input)
	var out bytes.Buffer

	handler := HandlerFunc(func(ctx context.Context, req JSONRPCRequest) (JSONRPCResponse, error) {
		return JSONRPCResponse{}, errors.New("something went wrong")
	})

	err := ServeStdio(context.Background(), in, &out, slog.Default(), handler)
	require.NoError(t, err)

	var resp JSONRPCResponse
	err = json.Unmarshal(out.Bytes(), &resp)
	require.NoError(t, err)
	assert.InDelta(t, float64(4), resp.ID, 0)
	require.NotNil(t, resp.Error)
	assert.Equal(t, -32603, resp.Error.Code)
	assert.Contains(t, resp.Error.Message, "something went wrong")
}

func TestServeStdio_BlankLinesSkipped(t *testing.T) {
	input := "\n  \n\t\n" +
		`{"jsonrpc":"2.0","id":5,"method":"ping"}` + "\n\n"
	in := strings.NewReader(input)
	var out bytes.Buffer

	handler := HandlerFunc(func(ctx context.Context, req JSONRPCRequest) (JSONRPCResponse, error) {
		return JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  "pong",
		}, nil
	})

	err := ServeStdio(context.Background(), in, &out, slog.Default(), handler)
	require.NoError(t, err)

	var resp JSONRPCResponse
	err = json.Unmarshal(out.Bytes(), &resp)
	require.NoError(t, err)
	assert.InDelta(t, float64(5), resp.ID, 0)
}

func TestServeStdio_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	input := `{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n"
	in := strings.NewReader(input)
	var out bytes.Buffer

	handler := HandlerFunc(func(ctx context.Context, req JSONRPCRequest) (JSONRPCResponse, error) {
		return JSONRPCResponse{}, nil
	})

	err := ServeStdio(ctx, in, &out, slog.Default(), handler)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestSendError(t *testing.T) {
	var buf bytes.Buffer
	err := SendError(&buf, 20, -32600, "invalid request")
	require.NoError(t, err)

	var resp JSONRPCResponse
	require.NoError(t, json.Unmarshal(buf.Bytes(), &resp))
	assert.InDelta(t, float64(20), resp.ID, 0)
	require.NotNil(t, resp.Error)
	assert.Equal(t, -32600, resp.Error.Code)
	assert.Equal(t, "invalid request", resp.Error.Message)
}

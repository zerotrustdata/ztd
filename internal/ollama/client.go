// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package ollama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/httpclient"
)

// Client is a minimal HTTP client for Ollama model maintenance endpoints.
type Client struct {
	base *url.URL
	http *http.Client
}

// ProgressResponse is one NDJSON progress event from /api/pull.
type ProgressResponse struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// NewClient constructs a client for the given Ollama base endpoint.
func NewClient(base *url.URL, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{base: base, http: httpClient}
}

// NewMaintenanceClient constructs a client for pull and copy. Its HTTP client
// sets no Timeout: http.Client.Timeout also covers reading the response, and a
// non-streaming pull answers only when the download ends. Each request's context
// owns the deadline.
func NewMaintenanceClient(base *url.URL) *Client {
	return NewClient(base, &http.Client{Transport: httpclient.NewIPv4Transport(nil)})
}

// pullRequest is the /api/pull body. Stream is explicit so a false value
// is not dropped; Ollama defaults a missing stream flag to true.
type pullRequest struct {
	Model  string `json:"model"`
	Stream bool   `json:"stream"`
}

// Pull downloads a model from the remote Ollama provider.
func (c *Client) Pull(ctx context.Context, model string, fn func(ProgressResponse) error) error {
	return c.stream(ctx, http.MethodPost, "/api/pull", pullRequest{
		Model:  model,
		Stream: false,
	}, fn)
}

// copyRequest is the /api/copy body. Fields are ordered by JSON key so the
// encoded bytes match the previous sorted-key map output.
type copyRequest struct {
	Destination string `json:"destination"`
	Source      string `json:"source"`
}

// Copy creates a model alias from an existing local model.
func (c *Client) Copy(ctx context.Context, source, destination string) error {
	return c.do(ctx, http.MethodPost, "/api/copy", copyRequest{
		Destination: destination,
		Source:      source,
	})
}

func (c *Client) do(ctx context.Context, method, path string, reqData any) error {
	body, err := json.Marshal(reqData)
	if err != nil {
		return fmt.Errorf("ollama: encode %s request: %w", path, err)
	}

	request, err := http.NewRequestWithContext(ctx, method, c.base.JoinPath(path).String(), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("ollama: build %s request: %w", path, err)
	}
	request.Header.Set(constants.HeaderContentType, constants.HeaderValueApplicationJSON)
	request.Header.Set(constants.HeaderAccept, constants.HeaderValueApplicationJSON)

	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("ollama: send %s request: %w", path, err)
	}
	defer response.Body.Close()

	respBody, err := io.ReadAll(response.Body)
	if err != nil {
		return fmt.Errorf("ollama: read %s response: %w", path, err)
	}
	return checkResponse(response.StatusCode, respBody)
}

func (c *Client) stream(ctx context.Context, method, path string, reqData any, fn func(ProgressResponse) error) error {
	body, err := json.Marshal(reqData)
	if err != nil {
		return fmt.Errorf("ollama: encode %s request: %w", path, err)
	}

	request, err := http.NewRequestWithContext(ctx, method, c.base.JoinPath(path).String(), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("ollama: build %s request: %w", path, err)
	}
	request.Header.Set(constants.HeaderContentType, constants.HeaderValueApplicationJSON)
	request.Header.Set(constants.HeaderAccept, constants.HeaderValueApplicationNDJSON)

	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("ollama: send %s request: %w", path, err)
	}
	defer response.Body.Close()

	if response.StatusCode >= http.StatusBadRequest {
		respBody, err := io.ReadAll(response.Body)
		if err != nil {
			return fmt.Errorf("ollama: read %s error response: %w", path, err)
		}
		return checkResponse(response.StatusCode, respBody)
	}

	scanner := bufio.NewScanner(response.Body)
	for scanner.Scan() {
		var progress ProgressResponse
		if err := json.Unmarshal(scanner.Bytes(), &progress); err != nil {
			return fmt.Errorf("%w: decode %s progress: %w", constants.ErrInferenceProviderResponseInvalid, path, err)
		}
		if progress.Error != "" {
			return fmt.Errorf("%w: %s", constants.ErrOllamaPull, progress.Error)
		}
		if fn == nil {
			continue
		}
		if err := fn(progress); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("ollama: read %s response: %w", path, err)
	}
	return nil
}

func checkResponse(statusCode int, body []byte) error {
	if statusCode < http.StatusBadRequest {
		return nil
	}
	var apiError struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &apiError); err == nil && apiError.Error != "" {
		return fmt.Errorf("%w: %s", constants.ErrOllamaAPI, apiError.Error)
	}
	return fmt.Errorf("%w: status %d", constants.ErrOllamaAPI, statusCode)
}

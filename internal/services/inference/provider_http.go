// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package inference

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/httpclient"
)

// providerHTTP is the bounded HTTP plumbing shared by the provider clients.
// component names the client in every error it returns. headers are set on
// every request; a hosted client carries its API key there, never in the URL,
// so a transport error that quotes the URL cannot leak it.
type providerHTTP struct {
	component        string
	base             *url.URL
	client           *http.Client
	headers          http.Header
	maxResponseBytes int64
}

// newProviderHTTP validates endpoint, an absolute http or https URL with a
// host. A path prefix is preserved, so an endpoint behind a reverse-proxied
// prefix such as http://host/ollama resolves API paths beneath it.
func newProviderHTTP(component, endpoint string) (providerHTTP, error) {
	base, err := url.Parse(endpoint)
	if err != nil {
		return providerHTTP{}, fmt.Errorf("%s: endpoint: %w: %w", component, constants.ErrInferenceEndpointInvalid, err)
	}
	if (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return providerHTTP{}, fmt.Errorf("%s: endpoint %q: %w", component, endpoint, constants.ErrInferenceEndpointInvalid)
	}
	return providerHTTP{
		component: component,
		base:      base,
		client: &http.Client{
			Timeout: ProviderRequestTimeout,
		},
		maxResponseBytes: defaultMaxResponseBytes,
	}, nil
}

// newHostedProviderHTTP is newProviderHTTP for a hosted provider API: an https
// origin, credential headers on every request, and redirects refused, because
// Go forwards custom credential headers across a redirect to another host.
func newHostedProviderHTTP(component, endpoint string, headers http.Header) (providerHTTP, error) {
	p, err := newProviderHTTP(component, endpoint)
	if err != nil {
		return providerHTTP{}, err
	}
	p.client = httpclient.WithoutRedirects(p.client, http.ErrUseLastResponse)
	p.headers = headers.Clone()
	return p, nil
}

// newRequest builds a provider request carrying the client's headers.
func (p *providerHTTP) newRequest(ctx context.Context, method, target string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, err
	}
	for name, values := range p.headers {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	return req, nil
}

// getJSON issues a GET against a provider API path and decodes the bounded
// JSON response into out. A non-200 status is ErrInferenceBackendUnavailable;
// the error body is drained for connection reuse and never surfaced.
func (p *providerHTTP) getJSON(ctx context.Context, op string, out any, apiPath ...string) error {
	return p.getJSONQuery(ctx, op, out, nil, apiPath...)
}

// getJSONQuery is getJSON with URL query parameters.
func (p *providerHTTP) getJSONQuery(ctx context.Context, op string, out any, query url.Values, apiPath ...string) error {
	target := p.base.JoinPath(apiPath...)
	target.RawQuery = query.Encode()
	httpReq, err := p.newRequest(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return fmt.Errorf("%s: %s: build request: %w", p.component, op, err)
	}
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return transportError(ctx, p.component, op, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBodyBytes))
		return fmt.Errorf("%s: %s: %w: status %d", p.component, op, constants.ErrInferenceBackendUnavailable, resp.StatusCode)
	}
	return p.decodeResponse(op, resp.Body, out)
}

// postJSON marshals in, POSTs it to a provider API path, and returns the
// response for the caller to classify. The caller closes the body.
func (p *providerHTTP) postJSON(ctx context.Context, op string, in any, apiPath ...string) (*http.Response, error) {
	body, err := json.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("%s: %s: marshal request: %w", p.component, op, err)
	}
	httpReq, err := p.newRequest(ctx, http.MethodPost, p.apiURL(apiPath...), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%s: %s: build request: %w", p.component, op, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, transportError(ctx, p.component, op, err)
	}
	return resp, nil
}

// Close releases the client's idle provider connections.
func (p *providerHTTP) Close() error {
	p.client.CloseIdleConnections()
	return nil
}

// apiURL resolves a provider API path against the validated base endpoint.
func (p *providerHTTP) apiURL(elem ...string) string {
	return p.base.JoinPath(elem...).String()
}

// decodeResponse reads a bounded provider response body and unmarshals it.
// Oversized or malformed bodies fail closed with
// ErrInferenceProviderResponseInvalid; the raw body text is never included
// in the error.
func (p *providerHTTP) decodeResponse(op string, body io.Reader, out any) error {
	data, err := io.ReadAll(io.LimitReader(body, p.maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("%s: %s: %w: %w", p.component, op, constants.ErrInferenceProviderResponseInvalid, err)
	}
	if int64(len(data)) > p.maxResponseBytes {
		return fmt.Errorf("%s: %s: %w: response exceeds %d bytes", p.component, op, constants.ErrInferenceProviderResponseInvalid, p.maxResponseBytes)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%s: %s: %w: %w", p.component, op, constants.ErrInferenceProviderResponseInvalid, err)
	}
	return nil
}

// awaitModelsAbsent verifies a released model set has left provider
// residency. Neither provider emits an unload event, and both acknowledge an
// unload before the runner stops, so residency is re-read until the set is
// absent or ProviderStatusTimeout elapses.
func awaitModelsAbsent(ctx context.Context, component string, readResidency func(context.Context) (ProviderResidency, error), models []string) error {
	ctx, cancel := context.WithTimeout(ctx, ProviderStatusTimeout)
	defer cancel()
	var resident string
	for {
		if ctx.Err() != nil {
			if !errors.Is(ctx.Err(), context.Canceled) && resident != "" {
				return fmt.Errorf("%s: verify released models absent: %w: model %q remains resident", component, constants.ErrInferenceModelReleaseFailed, resident)
			}
			return fmt.Errorf("%s: verify released models absent: %w", component, ctx.Err())
		}
		residency, err := readResidency(ctx)
		if err != nil {
			if (errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, constants.ErrInferenceBackendTimeout) || errors.Is(err, context.DeadlineExceeded)) && resident != "" {
				return fmt.Errorf("%s: verify released models absent: %w: model %q remains resident", component, constants.ErrInferenceModelReleaseFailed, resident)
			}
			return fmt.Errorf("%s: verify released models absent: %w", component, err)
		}
		resident = ""
		for _, model := range residency.Models {
			if slices.Contains(models, model.Name) {
				resident = model.Name
				break
			}
		}
		if resident == "" {
			return nil
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.Canceled) {
				return fmt.Errorf("%s: verify released models absent: %w", component, ctx.Err())
			}
			return fmt.Errorf("%s: verify released models absent: %w: model %q remains resident", component, constants.ErrInferenceModelReleaseFailed, resident)
		case <-time.After(providerUnloadRecheckInterval):
		}
	}
}

// transportError classifies a failed provider call. Caller cancellation
// stays distinguishable as context.Canceled; a context or client deadline
// is ErrInferenceBackendTimeout; anything else is
// ErrInferenceBackendUnavailable with the underlying transport cause
// preserved in the chain. component names the calling client.
func transportError(ctx context.Context, component, op string, err error) error {
	var netErr net.Error
	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		return fmt.Errorf("%s: %s: %w: %w", component, op, context.Canceled, err)
	case errors.Is(ctx.Err(), context.DeadlineExceeded), errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return fmt.Errorf("%s: %s: %w: %w", component, op, constants.ErrInferenceBackendTimeout, err)
	default:
		return fmt.Errorf("%s: %s: %w: %w", component, op, constants.ErrInferenceBackendUnavailable, err)
	}
}

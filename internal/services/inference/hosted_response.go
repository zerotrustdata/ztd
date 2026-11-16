// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package inference

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// hostedResponse is one captured hosted provider response: its exact bytes and
// the elapsed time from the request start to the arrival of its last bytes.
type hostedResponse struct {
	body       []byte
	completeNS *int64
}

// hostedTiming is the Operator-observed timing of one hosted generation. A
// hosted provider reports no internal timing, so the Inference Operator
// measures it at its egress boundary. Completion tokens include reasoning the
// provider may never stream, so the generation span starts when the request is
// sent, before generation begins, and ends at the last response byte; it
// includes prompt processing and network transfer. Starting it at the first
// visible part would leave hidden reasoning outside the span and overstate
// throughput. The total duration is the same span.
type hostedTiming struct {
	firstToken *int64
	generation *int64
	total      *int64
	source     operatorv1.InferenceTimingSource
}

func newHostedTiming(firstToken, complete *int64) hostedTiming {
	timing := hostedTiming{firstToken: firstToken, generation: complete, total: complete}
	if firstToken != nil || complete != nil {
		timing.source = operatorv1.InferenceTimingSource_INFERENCE_TIMING_SOURCE_PROVIDER
	}
	return timing
}

// consumeHostedResponse captures exactly one HTTP response independently of its
// decoder. decode runs only for a 200 response and reports whether repetition
// stopped generation. Joining capture keeps the raw tail even on decode failure.
// The caller owns the response body and classifies non-200 provider errors.
func (p *providerHTTP) consumeHostedResponse(ctx context.Context, resp *http.Response, providerAttemptID string, startedAt time.Time, stopGeneration context.CancelFunc, decode func(*providerResponseCapture) (bool, error)) (hostedResponse, error) {
	capture := newProviderResponseCapture(p.maxResponseBytes, startedAt)
	go capture.receive(resp.Body)
	var stoppedRepeating bool
	var decodeErr error
	if resp.StatusCode == http.StatusOK {
		stoppedRepeating, decodeErr = decode(capture)
	}
	if stoppedRepeating {
		stopGeneration()
	}
	responseBody, readErr := capture.result()
	if stoppedRepeating && ctx.Err() == nil {
		readErr = nil
	}
	// Only successful HTTP streams bind raw bytes to an attempt, by digest and
	// size. An error body can echo prompt material and is never recorded.
	if store := attemptStoreFromContext(ctx); store != nil && resp.StatusCode == http.StatusOK {
		if err := store.RecordRawResponse(context.WithoutCancel(ctx), providerAttemptID, responseBody); err != nil {
			return hostedResponse{}, errors.Join(readErr, fmt.Errorf("%s: record raw response digest: %w", p.component, err))
		}
	}
	if readErr != nil {
		return hostedResponse{}, transportError(ctx, p.component, "generate", readErr)
	}
	if int64(len(responseBody)) > p.maxResponseBytes {
		return hostedResponse{}, fmt.Errorf("%s: generate: %w: response exceeds %d bytes", p.component, constants.ErrInferenceProviderResponseInvalid, p.maxResponseBytes)
	}
	if decodeErr != nil {
		return hostedResponse{}, decodeErr
	}
	return hostedResponse{body: responseBody, completeNS: capture.finalArrival()}, nil
}

// hostedResponseEvidence holds the provider facts needed to bind one generation
// to its metadata identity and exact request and response bytes.
type hostedResponseEvidence struct {
	servedModel           string
	providerModelVersion  string
	identityDigest        string
	responseID            string
	normalizedRequestHash string
	rawResponse           []byte
}

// hostedEgressAttestation refuses a reroute for a pinned model, then assembles
// the common evidence. The served digest is set only for a pinned generation.
func hostedEgressAttestation(component, origin string, req models.GenerateRequest, evidence hostedResponseEvidence) (*operatorv1.InferenceHostedEgressAttestation, string, error) {
	if req.ModelDigest != "" && evidence.servedModel != req.Model {
		return nil, "", fmt.Errorf("%s: generate: provider served %q for %q: %w", component, evidence.servedModel, req.Model, constants.ErrInferenceModelDigestMismatch)
	}
	servedModelDigest := ""
	if req.ModelDigest != "" {
		servedModelDigest = evidence.identityDigest
	}
	return &operatorv1.InferenceHostedEgressAttestation{
		ProviderOrigin:        origin,
		ModelVersion:          evidence.servedModel,
		Provenance:            constants.HostedModelProvenance,
		NormalizedRequestHash: evidence.normalizedRequestHash,
		ExplicitConsent:       true,
		ProviderClass:         req.ProviderClass,
		RequestedModel:        req.Model,
		ProviderModelVersion:  evidence.providerModelVersion,
		ModelIdentityDigest:   evidence.identityDigest,
		ProviderResponseId:    evidence.responseID,
		RawResponseDigest:     models.SHA256Hex(evidence.rawResponse),
		RawResponseSizeBytes:  uint64(len(evidence.rawResponse)),
	}, servedModelDigest, nil
}

// nextSSEData returns the data of the next server-sent event and the bytes
// consumed reading it. Multiple data lines join with a newline; comments and
// other fields are skipped. It returns io.EOF once no event remains.
func nextSSEData(reader *bufio.Reader) ([]byte, int64, error) {
	var data []byte
	var consumed int64
	have := false
	for {
		line, err := reader.ReadBytes('\n')
		consumed += int64(len(line))
		trimmed := bytes.TrimRight(line, "\r\n")
		switch {
		case len(line) == 0:
		case len(trimmed) == 0:
			if have {
				return data, consumed, nil
			}
		case trimmed[0] == ':':
		default:
			field, value, _ := bytes.Cut(trimmed, []byte(":"))
			if string(field) == "data" {
				if have {
					data = append(data, '\n')
				}
				data = append(data, bytes.TrimPrefix(value, []byte(" "))...)
				have = true
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) && have {
				return data, consumed, nil
			}
			return nil, consumed, err
		}
	}
}

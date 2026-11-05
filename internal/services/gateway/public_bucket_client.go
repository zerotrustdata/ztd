// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/httpclient"
	"github.com/zerotrustdata/ztd/internal/models"
)

const (
	publicBucketSigningService = "s3"
	// publicBucketSigningRegion is the region R2 signs under.
	publicBucketSigningRegion  = "auto"
	publicBucketRequestTimeout = 5 * time.Minute
	publicBucketEmptySHA256    = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	publicBucketListMaxBytes   = 8 << 20
	publicCachePurgeAPIBase    = "https://api.cloudflare.com/client/v4"
	publicCachePurgeMaxBytes   = 1 << 20
)

// publicBucketHTTPClient writes to an S3-compatible API with path-style keys and
// SigV4. Errors carry object keys and status codes only.
type publicBucketHTTPClient struct {
	httpClient *http.Client
	endpoint   string
	bucket     string
	creds      aws.Credentials
	signer     *v4.Signer
}

func newPublicBucketHTTPClient(cfg models.PublicBucketConfig, creds models.PublicBucketCredentials) *publicBucketHTTPClient {
	return &publicBucketHTTPClient{
		httpClient: newPublicBucketHTTP(),
		endpoint:   strings.TrimRight(cfg.Endpoint, "/"),
		bucket:     cfg.Name,
		creds:      aws.Credentials{AccessKeyID: creds.AccessKeyID, SecretAccessKey: creds.SecretAccessKey},
		signer:     v4.NewSigner(func(options *v4.SignerOptions) { options.DisableURIPathEscaping = true }),
	}
}

func newPublicBucketHTTP() *http.Client {
	return &http.Client{Timeout: publicBucketRequestTimeout, Transport: httpclient.NewIPv4Transport(nil)}
}

func (c *publicBucketHTTPClient) objectURL(key string) string {
	segments := strings.Split(key, "/")
	for index, segment := range segments {
		segments[index] = url.PathEscape(segment)
	}
	return c.endpoint + "/" + url.PathEscape(c.bucket) + "/" + strings.Join(segments, "/")
}

// do signs and sends a request, returning the response when it succeeded.
func (c *publicBucketHTTPClient) do(ctx context.Context, operation string, request *http.Request, payloadSHA256 string) (*http.Response, error) {
	request.Header.Set("X-Amz-Content-Sha256", payloadSHA256)
	if err := c.signer.SignHTTP(ctx, c.creds, request, payloadSHA256, publicBucketSigningService, publicBucketSigningRegion, time.Now().UTC()); err != nil {
		return nil, fmt.Errorf("public-feed: bucket %s: sign: %w", operation, err)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", constants.ErrPublicBucketRequestFailed, operation, err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, publicCachePurgeMaxBytes))
		_ = response.Body.Close()
		return response, fmt.Errorf("%w: %s: status %d", constants.ErrPublicBucketRequestFailed, operation, response.StatusCode)
	}
	return response, nil
}

// Put uploads one object.
func (c *publicBucketHTTPClient) Put(ctx context.Context, put publicBucketPut) error {
	operation := "put " + put.Key
	var body io.Reader = http.NoBody
	if put.Size > 0 {
		body = io.NopCloser(put.Body)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, c.objectURL(put.Key), body)
	if err != nil {
		return fmt.Errorf("public-feed: bucket %s: %w", operation, err)
	}
	request.ContentLength = put.Size
	request.Header.Set(constants.HeaderContentType, put.ContentType)
	request.Header.Set(constants.HeaderCacheControl, put.CacheControl)
	if put.ContentEncoding != "" {
		request.Header.Set("Content-Encoding", put.ContentEncoding)
	}
	response, err := c.do(ctx, operation, request, put.SHA256)
	if err != nil {
		return err
	}
	return closeBucketResponse(response)
}

// Delete removes one object. Deleting an absent object succeeds, so a retried
// delete converges.
func (c *publicBucketHTTPClient) Delete(ctx context.Context, key string) error {
	operation := "delete " + key
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.objectURL(key), http.NoBody)
	if err != nil {
		return fmt.Errorf("public-feed: bucket %s: %w", operation, err)
	}
	response, err := c.do(ctx, operation, request, publicBucketEmptySHA256)
	if err != nil {
		if response != nil && response.StatusCode == http.StatusNotFound {
			return nil
		}
		return err
	}
	return closeBucketResponse(response)
}

type publicBucketListResponse struct {
	Contents []struct {
		Key          string    `xml:"Key"`
		LastModified time.Time `xml:"LastModified"`
	} `xml:"Contents"`
	IsTruncated           bool   `xml:"IsTruncated"`
	NextContinuationToken string `xml:"NextContinuationToken"`
}

// List returns every object under prefix, following continuation tokens.
func (c *publicBucketHTTPClient) List(ctx context.Context, prefix string) ([]publicBucketObject, error) {
	operation := "list " + prefix
	objects := []publicBucketObject{}
	token := ""
	for {
		query := url.Values{"list-type": {"2"}, "prefix": {prefix}}
		if token != "" {
			query.Set("continuation-token", token)
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+"/"+url.PathEscape(c.bucket)+"?"+query.Encode(), http.NoBody)
		if err != nil {
			return nil, fmt.Errorf("public-feed: bucket %s: %w", operation, err)
		}
		response, err := c.do(ctx, operation, request, publicBucketEmptySHA256)
		if err != nil {
			return nil, err
		}
		var page publicBucketListResponse
		decodeErr := xml.NewDecoder(io.LimitReader(response.Body, publicBucketListMaxBytes)).Decode(&page)
		_ = response.Body.Close()
		if decodeErr != nil {
			return nil, fmt.Errorf("%w: %s: decode: %w", constants.ErrPublicBucketRequestFailed, operation, decodeErr)
		}
		for _, entry := range page.Contents {
			objects = append(objects, publicBucketObject{Key: entry.Key, LastModified: entry.LastModified})
		}
		if !page.IsTruncated {
			return objects, nil
		}
		if page.NextContinuationToken == "" {
			return nil, fmt.Errorf("%w: %s: truncated listing has no continuation token", constants.ErrPublicBucketRequestFailed, operation)
		}
		token = page.NextContinuationToken
	}
}

func closeBucketResponse(response *http.Response) error {
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, publicCachePurgeMaxBytes))
	return response.Body.Close()
}

// publicCloudflarePurger purges URLs from one Cloudflare zone's cache.
type publicCloudflarePurger struct {
	httpClient *http.Client
	apiBase    string
	zoneID     string
	token      string
}

func newPublicCloudflarePurger(cfg models.PublicBucketConfig, creds models.PublicBucketCredentials) *publicCloudflarePurger {
	return &publicCloudflarePurger{httpClient: newPublicBucketHTTP(), apiBase: publicCachePurgeAPIBase, zoneID: cfg.ZoneID, token: creds.PurgeToken}
}

type publicCachePurgeRequest struct {
	Files []string `json:"files"`
}

type publicCachePurgeResponse struct {
	Success bool `json:"success"`
}

// Purge removes urls from the cache.
func (p *publicCloudflarePurger) Purge(ctx context.Context, urls []string) error {
	body, err := json.Marshal(publicCachePurgeRequest{Files: urls})
	if err != nil {
		return fmt.Errorf("public-feed: encode cache purge: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.apiBase+"/zones/"+url.PathEscape(p.zoneID)+"/purge_cache", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("public-feed: build cache purge: %w", err)
	}
	request.Header.Set(constants.HeaderContentType, constants.HeaderValueApplicationJSON)
	request.Header.Set("Authorization", "Bearer "+p.token)
	response, err := p.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("%w: %d urls: %w", constants.ErrPublicBucketPurgeFailed, len(urls), err)
	}
	defer func() { _ = response.Body.Close() }()
	var result publicCachePurgeResponse
	decodeErr := json.NewDecoder(io.LimitReader(response.Body, publicCachePurgeMaxBytes)).Decode(&result)
	if response.StatusCode < 200 || response.StatusCode >= 300 || decodeErr != nil || !result.Success {
		return fmt.Errorf("%w: %d urls: status %d", constants.ErrPublicBucketPurgeFailed, len(urls), response.StatusCode)
	}
	return nil
}

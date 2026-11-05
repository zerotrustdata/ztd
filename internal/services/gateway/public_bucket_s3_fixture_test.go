//go:build integration

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License.

package gateway

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

// This file's server stands in only for the external bucket and the cache purge
// API (INV-TEST-RUN-03). Everything else in the publish path is real.

const (
	bucketS3TestBucket     = "feed-bucket"
	bucketS3TestAccessKey  = "bucket-access-key-0001"
	bucketS3TestSecret     = "bucket-secret-value-0001"
	bucketS3TestPurgeToken = "bucket-purge-token-0001"
	bucketS3TestZoneID     = "zone-bucket-integration"
	bucketS3TestPublicHost = "https://feed.example"
	bucketS3TestRegion     = "auto"
	bucketS3TestService    = "s3"
	bucketS3TestDateLayout = "20060102T150405Z"
	bucketS3TestPageSize   = 2
	bucketS3TestPurgePath  = "/zones/" + bucketS3TestZoneID + "/purge_cache"
)

// bucketS3Object is one object the fake bucket stores.
type bucketS3Object struct {
	body            []byte
	contentType     string
	contentEncoding string
	cacheControl    string
	lastModified    time.Time
}

// bucketS3Fixture is an S3-compatible bucket and a Cloudflare purge endpoint in
// one httptest server each. Every S3 request is verified against the secret.
type bucketS3Fixture struct {
	t       *testing.T
	server  *httptest.Server
	purge   *httptest.Server
	signer  *v4.Signer
	mu      sync.Mutex
	objects map[string]bucketS3Object
	// failPuts counts the remaining PUTs to each key that answer 500.
	failPuts   map[string]int
	purgeFail  bool
	purgedURLs []string
}

// newBucketS3Fixture starts the fake bucket and purge endpoint. Both close when
// the test ends.
func newBucketS3Fixture(t *testing.T) *bucketS3Fixture {
	t.Helper()
	fixture := &bucketS3Fixture{
		t:        t,
		signer:   v4.NewSigner(func(options *v4.SignerOptions) { options.DisableURIPathEscaping = true }),
		objects:  map[string]bucketS3Object{},
		failPuts: map[string]int{},
	}
	fixture.server = httptest.NewServer(http.HandlerFunc(fixture.serveS3))
	t.Cleanup(fixture.server.Close)
	fixture.purge = httptest.NewServer(http.HandlerFunc(fixture.servePurge))
	t.Cleanup(fixture.purge.Close)
	return fixture
}

// bucketConfig returns the bucket configuration that points at this fixture.
func (f *bucketS3Fixture) bucketConfig() models.PublicBucketConfig {
	return models.PublicBucketConfig{
		Name:         bucketS3TestBucket,
		Endpoint:     f.server.URL,
		PublicOrigin: bucketS3TestPublicHost,
		ZoneID:       bucketS3TestZoneID,
	}
}

// credentials returns the bucket credentials the fixture accepts.
func (f *bucketS3Fixture) credentials() models.PublicBucketCredentials {
	return models.PublicBucketCredentials{
		AccessKeyID:     bucketS3TestAccessKey,
		SecretAccessKey: bucketS3TestSecret,
		PurgeToken:      bucketS3TestPurgeToken,
	}
}

// newClient returns the production S3 client aimed at this fixture.
func (f *bucketS3Fixture) newClient(creds models.PublicBucketCredentials) *publicBucketHTTPClient {
	return newPublicBucketHTTPClient(f.bucketConfig(), creds)
}

// newPurger returns the production purger aimed at this fixture's purge endpoint.
func (f *bucketS3Fixture) newPurger(creds models.PublicBucketCredentials) *publicCloudflarePurger {
	purger := newPublicCloudflarePurger(f.bucketConfig(), creds)
	purger.apiBase = f.purge.URL
	return purger
}

// object returns one stored object.
func (f *bucketS3Fixture) object(key string) (bucketS3Object, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	object, ok := f.objects[key]
	return object, ok
}

// keys returns every stored key with prefix, sorted.
func (f *bucketS3Fixture) keys(prefix string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var keys []string
	for key := range f.objects {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	return keys
}

// failPut makes the next count PUTs to key answer 500.
func (f *bucketS3Fixture) failPut(key string, count int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failPuts[key] = count
}

// setPurgeFail makes the purge endpoint answer 500 while fail is set.
func (f *bucketS3Fixture) setPurgeFail(fail bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.purgeFail = fail
}

// purged returns every URL the purge endpoint accepted.
func (f *bucketS3Fixture) purged() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.purgedURLs)
}

// serveS3 authenticates a request, then routes it to the bucket operation.
func (f *bucketS3Fixture) serveS3(w http.ResponseWriter, r *http.Request) {
	if err := f.verifySignature(r); err != nil {
		writeBucketS3Error(w, http.StatusForbidden, "SignatureDoesNotMatch", err.Error())
		return
	}
	prefix := "/" + bucketS3TestBucket
	switch {
	case r.Method == http.MethodGet && r.URL.Path == prefix && r.URL.Query().Get("list-type") == "2":
		f.serveList(w, r)
	case r.URL.Path == prefix || !strings.HasPrefix(r.URL.Path, prefix+"/"):
		writeBucketS3Error(w, http.StatusNotFound, "NoSuchBucket", "unknown bucket path")
	case r.Method == http.MethodPut:
		f.servePut(w, r, strings.TrimPrefix(r.URL.Path, prefix+"/"))
	case r.Method == http.MethodGet:
		f.serveGet(w, strings.TrimPrefix(r.URL.Path, prefix+"/"))
	case r.Method == http.MethodDelete:
		f.mu.Lock()
		delete(f.objects, strings.TrimPrefix(r.URL.Path, prefix+"/"))
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	default:
		writeBucketS3Error(w, http.StatusMethodNotAllowed, "MethodNotAllowed", r.Method)
	}
}

// verifySignature rebuilds the request with the production signer and secret,
// then compares the Authorization header. The signing time is the request's own
// X-Amz-Date, and the payload hash is the request's X-Amz-Content-Sha256.
func (f *bucketS3Fixture) verifySignature(r *http.Request) error {
	authorization := r.Header.Get("Authorization")
	if authorization == "" {
		return fmt.Errorf("request is not signed")
	}
	signedAt, err := time.Parse(bucketS3TestDateLayout, r.Header.Get("X-Amz-Date"))
	if err != nil {
		return fmt.Errorf("missing or malformed X-Amz-Date: %w", err)
	}
	// Only the headers the client declared as signed take part in the rebuilt
	// signature. A transport may add headers after signing, such as
	// Accept-Encoding, and those are not part of the request's signature.
	signed, err := bucketS3SignedHeaderNames(authorization)
	if err != nil {
		return err
	}
	rebuilt := r.Clone(r.Context())
	rebuilt.Header = http.Header{}
	for _, name := range signed {
		for _, value := range r.Header.Values(name) {
			rebuilt.Header.Add(name, value)
		}
	}
	rebuilt.ContentLength = r.ContentLength
	creds := aws.Credentials{AccessKeyID: bucketS3TestAccessKey, SecretAccessKey: bucketS3TestSecret}
	payloadHash := r.Header.Get("X-Amz-Content-Sha256")
	if err := f.signer.SignHTTP(r.Context(), creds, rebuilt, payloadHash, bucketS3TestService, bucketS3TestRegion, signedAt); err != nil {
		return fmt.Errorf("rebuild signature: %w", err)
	}
	if rebuilt.Header.Get("Authorization") != authorization {
		return fmt.Errorf("authorization does not match the secret")
	}
	return nil
}

// bucketS3SignedHeaderNames returns the lower-case header names listed in the
// SignedHeaders field of an AWS4-HMAC-SHA256 Authorization header.
func bucketS3SignedHeaderNames(authorization string) ([]string, error) {
	_, fields, found := strings.Cut(authorization, "SignedHeaders=")
	if !found {
		return nil, fmt.Errorf("authorization lists no SignedHeaders")
	}
	list, _, _ := strings.Cut(fields, ",")
	names := strings.Split(strings.TrimSpace(list), ";")
	if len(names) == 0 || names[0] == "" {
		return nil, fmt.Errorf("authorization lists no SignedHeaders")
	}
	return names, nil
}

// servePut stores an object after checking the payload digest the client
// declared against the bytes it sent.
func (f *bucketS3Fixture) servePut(w http.ResponseWriter, r *http.Request, key string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeBucketS3Error(w, http.StatusBadRequest, "IncompleteBody", err.Error())
		return
	}
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != r.Header.Get("X-Amz-Content-Sha256") {
		writeBucketS3Error(w, http.StatusBadRequest, "XAmzContentSHA256Mismatch", key)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failPuts[key] > 0 {
		f.failPuts[key]--
		writeBucketS3Error(w, http.StatusInternalServerError, "InternalError", key)
		return
	}
	f.objects[key] = bucketS3Object{
		body:            body,
		contentType:     r.Header.Get(constants.HeaderContentType),
		contentEncoding: r.Header.Get("Content-Encoding"),
		cacheControl:    r.Header.Get(constants.HeaderCacheControl),
		lastModified:    time.Now().UTC(),
	}
	w.WriteHeader(http.StatusOK)
}

func (f *bucketS3Fixture) serveGet(w http.ResponseWriter, key string) {
	object, ok := f.object(key)
	if !ok {
		writeBucketS3Error(w, http.StatusNotFound, "NoSuchKey", key)
		return
	}
	if object.contentType != "" {
		w.Header().Set(constants.HeaderContentType, object.contentType)
	}
	if object.contentEncoding != "" {
		w.Header().Set("Content-Encoding", object.contentEncoding)
	}
	if object.cacheControl != "" {
		w.Header().Set(constants.HeaderCacheControl, object.cacheControl)
	}
	w.Header().Set("Content-Length", fmt.Sprint(len(object.body)))
	_, _ = w.Write(object.body)
}

// bucketS3ListResult is the ListObjectsV2 response body.
type bucketS3ListResult struct {
	XMLName               xml.Name            `xml:"ListBucketResult"`
	Name                  string              `xml:"Name"`
	Prefix                string              `xml:"Prefix"`
	KeyCount              int                 `xml:"KeyCount"`
	MaxKeys               int                 `xml:"MaxKeys"`
	IsTruncated           bool                `xml:"IsTruncated"`
	NextContinuationToken string              `xml:"NextContinuationToken,omitempty"`
	Contents              []bucketS3ListEntry `xml:"Contents"`
}

type bucketS3ListEntry struct {
	Key          string    `xml:"Key"`
	LastModified time.Time `xml:"LastModified"`
	ETag         string    `xml:"ETag"`
	Size         int64     `xml:"Size"`
}

// serveList pages through matching keys. The continuation token is the last key
// returned, so the next page starts after it.
func (f *bucketS3Fixture) serveList(w http.ResponseWriter, r *http.Request) {
	prefix := r.URL.Query().Get("prefix")
	token := r.URL.Query().Get("continuation-token")
	f.mu.Lock()
	var matching []string
	for key := range f.objects {
		if strings.HasPrefix(key, prefix) && (token == "" || key > token) {
			matching = append(matching, key)
		}
	}
	slices.Sort(matching)
	result := bucketS3ListResult{Name: bucketS3TestBucket, Prefix: prefix, MaxKeys: bucketS3TestPageSize}
	if len(matching) > bucketS3TestPageSize {
		matching = matching[:bucketS3TestPageSize]
		result.IsTruncated = true
		result.NextContinuationToken = matching[len(matching)-1]
	}
	for _, key := range matching {
		object := f.objects[key]
		result.Contents = append(result.Contents, bucketS3ListEntry{
			Key:          key,
			LastModified: object.lastModified,
			ETag:         `"` + hex.EncodeToString(sha256Sum(object.body)) + `"`,
			Size:         int64(len(object.body)),
		})
	}
	f.mu.Unlock()
	result.KeyCount = len(result.Contents)
	w.Header().Set(constants.HeaderContentType, "application/xml")
	_ = xml.NewEncoder(w).Encode(result)
}

func sha256Sum(body []byte) []byte {
	sum := sha256.Sum256(body)
	return sum[:]
}

// servePurge accepts a cache purge when the bearer token matches and purge is
// not set to fail.
func (f *bucketS3Fixture) servePurge(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(constants.HeaderContentType, constants.HeaderValueApplicationJSON)
	if r.Method != http.MethodPost || r.URL.Path != bucketS3TestPurgePath {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"success":false}`))
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+bucketS3TestPurgeToken {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"success":false}`))
		return
	}
	var request publicCachePurgeRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"success":false}`))
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.purgeFail {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"success":false}`))
		return
	}
	f.purgedURLs = append(f.purgedURLs, request.Files...)
	_, _ = w.Write([]byte(`{"success":true}`))
}

// writeBucketS3Error writes an S3-style XML error.
func writeBucketS3Error(w http.ResponseWriter, status int, code, message string) {
	var body bytes.Buffer
	body.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	fmt.Fprintf(&body, "<Error><Code>%s</Code><Message>%s</Message></Error>", code, message)
	w.Header().Set(constants.HeaderContentType, "application/xml")
	w.WriteHeader(status)
	_, _ = w.Write(body.Bytes())
}

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package conformancetest

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// fatalRecorder stands in for a test so that a failed pin can be observed without
// failing the enclosing test. Only Helper and Fatalf are called on it.
type fatalRecorder struct {
	testing.TB
	fatal string
}

func (f *fatalRecorder) Helper() {}

func (f *fatalRecorder) Fatalf(format string, args ...any) {
	f.fatal = fmt.Sprintf(format, args...)
}

func gzipBytes(t *testing.T, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	writer, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	require.NoError(t, err)
	_, err = writer.Write(payload)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	return buf.Bytes()
}

func TestDoctrineRecordIsTheCanonicalRecordOfTheOutboundDataOperatorRun(t *testing.T) {
	raw, digest := DoctrineRecord(t)

	record := &evalv1.ConformanceRunRecord{}
	require.NoError(t, evalv1.UnmarshalCanonical(raw, record))
	assert.Equal(t, "c1f5cb4c-f285-4bfd-bcf0-303bcdbd327f", record.GetRunId())
	assert.False(t, record.GetOutboundAbsent())
	assert.Equal(t, doctrineRecordSHA256, digest)
}

func TestEmbeddedDoctrineRecordRanEveryCaseOnTheEmbeddedExecutor(t *testing.T) {
	raw, digest := EmbeddedDoctrineRecord(t)

	record := &evalv1.ConformanceRunRecord{}
	require.NoError(t, evalv1.UnmarshalCanonical(raw, record))
	assert.Equal(t, "573603cf-0a56-4d42-a242-7a2d145b62dc", record.GetRunId())
	assert.True(t, record.GetOutboundAbsent())
	assert.Equal(t, embeddedDoctrineRecordSHA256, digest)
	require.NotEmpty(t, record.GetCases())
	for _, evidence := range record.GetCases() {
		assert.Equal(t, "embedded", evidence.GetExecutor(), "case %s", evidence.GetCaseId())
	}
}

func TestDoctrineRecordReturnsFreshBytesOnEachCall(t *testing.T) {
	first, _ := DoctrineRecord(t)
	first[0] ^= 0xff

	second, _ := DoctrineRecord(t)

	assert.NotEqual(t, first, second, "a caller's mutation must not reach the next caller")
}

func TestDecodePinnedRecordRejectsTamperedPayloadAgainstPin(t *testing.T) {
	raw, digest := DoctrineRecord(t)
	tampered := append([]byte(nil), raw...)
	tampered[len(tampered)/2] ^= 0x01
	recorder := &fatalRecorder{}

	got, gotDigest := decodePinnedRecord(recorder, gzipBytes(t, tampered), digest)

	assert.Contains(t, recorder.fatal, "pinned doctrine record sha256", "a changed record fails the pin")
	assert.Nil(t, got)
	assert.Empty(t, gotDigest)
}

func TestDecodePinnedRecordRejectsWrongPinForUnchangedRecord(t *testing.T) {
	raw, _ := DoctrineRecord(t)
	recorder := &fatalRecorder{}

	got, _ := decodePinnedRecord(recorder, gzipBytes(t, raw), "00")

	assert.Contains(t, recorder.fatal, "want 00", "the pin is compared, not the record's own digest")
	assert.Nil(t, got)
}

func TestDecodePinnedRecordRejectsBytesThatAreNotGzip(t *testing.T) {
	recorder := &fatalRecorder{}

	got, _ := decodePinnedRecord(recorder, []byte("not gzip"), doctrineRecordSHA256)

	assert.Contains(t, recorder.fatal, "open pinned doctrine record", "a corrupt pin file fails closed")
	assert.Nil(t, got)
}

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package storage

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/timesvc"
)

func sampleAuditChainSegmentEntries(t *testing.T) []AuditChainSegmentEntry {
	t.Helper()
	timestamp := timesvc.FormatTimestamp(time.Unix(1_700_000_000, 0).UTC())
	digest := strings.Repeat("a1", 32)
	first := AuditChainSegmentEntry{
		Seq:           1,
		PrevHash:      auditChainGenesisPrevHash,
		EventType:     string(constants.EventOperatorReceiptRecorded),
		Timestamp:     timestamp,
		ContentDigest: digest,
		TransactionID: "tx-1",
	}
	first.Hash = AuditEventChainHash(first.Seq, first.PrevHash, first.EventType, "", first.Timestamp, first.ContentDigest, first.TransactionID)

	secondDigest := strings.Repeat("b2", 32)
	second := AuditChainSegmentEntry{
		Seq:           2,
		PrevHash:      first.Hash,
		EventType:     string(constants.EventOperatorReceiptRecorded),
		Timestamp:     timestamp,
		ContentDigest: secondDigest,
		TransactionID: "tx-2",
	}
	second.Hash = AuditEventChainHash(second.Seq, second.PrevHash, second.EventType, "", second.Timestamp, second.ContentDigest, second.TransactionID)
	return []AuditChainSegmentEntry{first, second}
}

func TestVerifyAuditChainSegment_AcceptsLinkedSegment(t *testing.T) {
	t.Parallel()
	entries := sampleAuditChainSegmentEntries(t)
	require.NoError(t, VerifyAuditChainSegment(entries, auditChainGenesisPrevHash, entries[1].Hash))
}

func TestVerifyAuditChainSegment_RejectsTamperedHash(t *testing.T) {
	t.Parallel()
	entries := sampleAuditChainSegmentEntries(t)
	entries[0].Hash = "tampered"
	err := VerifyAuditChainSegment(entries, auditChainGenesisPrevHash, entries[1].Hash)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hash mismatch")
}

func TestMarshalAuditChainSegmentEntry_RoundTrip(t *testing.T) {
	t.Parallel()
	entries := sampleAuditChainSegmentEntries(t)
	body, err := MarshalAuditChainSegmentEntry(entries[0])
	require.NoError(t, err)
	decoded, err := UnmarshalAuditChainSegmentEntry(body)
	require.NoError(t, err)
	assert.Equal(t, entries[0], decoded)
}

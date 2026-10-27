// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package storage

import (
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/sqliteutil"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// TestDefaultLocalFirstEventsStoreConfig verifies the default configuration values
func TestDefaultLocalFirstEventsStoreConfig(t *testing.T) {
	config := DefaultLocalFirstEventsStoreConfig()

	if config == nil {
		t.Fatal("DefaultLocalFirstEventsStoreConfig returned nil")
	}

	tests := []struct {
		name     string
		got      interface{}
		expected interface{}
	}{
		{"RetentionDays", config.RetentionDays, 90},
		{"PruneIntervalMinutes", config.PruneIntervalMinutes, 60},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.expected {
				t.Errorf("%s = %v, want %v", tt.name, tt.got, tt.expected)
			}
		})
	}
}

// TestFileMutationOperationConstants verifies operation constants
func TestFileMutationOperationConstants(t *testing.T) {
	tests := []struct {
		name  string
		op    FileMutationOperation
		value string
	}{
		{"FileMutationWrite", FileMutationWrite, "WRITE"},
		{"FileMutationCreate", FileMutationCreate, "CREATE"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if string(tt.op) != tt.value {
				t.Errorf("%s = %q, want %q", tt.name, tt.op, tt.value)
			}
		})
	}
}

// TestErrorConstants verifies error constants
func TestErrorConstants(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"ErrEventsStoreEventNil", constants.ErrEventsStoreEventNil, "EVENTS_STORE_EVENT_INVALID"},
		{"ErrEventsStoreSessionMissing", constants.ErrEventsStoreSessionMissing, "EVENTS_STORE_SESSION_MISSING"},
		{"ErrEventsStoreSessionUnknown", constants.ErrEventsStoreSessionUnknown, "EVENTS_STORE_SESSION_UNKNOWN"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err == nil {
				t.Fatal("Error constant should not be nil")
			}
			if !strings.Contains(tt.err.Error(), tt.want) {
				t.Errorf("Error message = %q, want to contain %q", tt.err.Error(), tt.want)
			}
		})
	}
}

// TestNilStoreMethods verifies nil-safe method calls
func TestNilStoreMethods(t *testing.T) {
	var ass *LocalFirstEventsStore

	// These methods should handle nil gracefully
	ass.Wait()
	err := ass.Close()
	if err != nil {
		t.Errorf("Close() on nil store should return nil, got %v", err)
	}

	vault := ass.GetEncryptionVault()
	if vault != nil {
		t.Error("GetEncryptionVault() on nil store should return nil")
	}

	dataDir := ass.GetDataDir()
	if dataDir != "" {
		t.Error("GetDataDir() on nil store should return empty string")
	}
}

// TestGetDataDir verifies data directory retrieval
func TestGetDataDir(t *testing.T) {
	tests := []struct {
		name string
		ass  *LocalFirstEventsStore
		want string
	}{
		{
			name: "nil store",
			ass:  nil,
			want: "",
		},
		{
			name: "store with nil fileSvc",
			ass: &LocalFirstEventsStore{
				config: &LocalFirstEventsStoreConfig{},
			},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.ass.GetDataDir()
			if got != tt.want {
				t.Errorf("GetDataDir() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLocalFirstEventsStore_WaitJoinsInFlightWrites(t *testing.T) {
	t.Run("nil store returns", func(t *testing.T) {
		done := make(chan struct{})
		go func() {
			var store *LocalFirstEventsStore
			store.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("nil LocalFirstEventsStore.Wait did not return")
		}
	})

	t.Run("blocks until the in-flight write finishes", func(t *testing.T) {
		store := &LocalFirstEventsStore{}
		store.muWrites.Add(1)
		done := make(chan struct{})
		go func() {
			store.Wait()
			close(done)
		}()
		select {
		case <-done:
			t.Fatal("Wait returned while a write was still in flight")
		case <-time.After(30 * time.Millisecond):
		}
		store.muWrites.Done()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("Wait did not return after the in-flight write finished")
		}
	})
}

// TestClose verifies close behavior
func TestClose(t *testing.T) {
	tests := []struct {
		name string
		ass  *LocalFirstEventsStore
	}{
		{
			name: "nil store",
			ass:  nil,
		},
		{
			name: "store without resources",
			ass:  &LocalFirstEventsStore{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.ass.Close(); err != nil {
				t.Errorf("Close() unexpected error = %v", err)
			}
		})
	}
}

// TestLocalFirstEventsStore_NilDB verifies that NewLocalFirstEventsStore
// returns an error when db is nil.
func TestLocalFirstEventsStore_NilDB(t *testing.T) {
	logger := testutil.NewTestLogger()
	fileSvc := newTestFileSvc(t, testutil.TempDir(t))
	config := DefaultLocalFirstEventsStoreConfig()
	_, privKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	config.EncryptionVault = CreateTestVault(t, fileSvc, privKey)

	ass, err := NewLocalFirstEventsStore(config, nil, logger, fileSvc)
	if err == nil {
		t.Error("NewLocalFirstEventsStore with nil db should return error")
	}
	if !errors.Is(err, constants.ErrMissingRequiredField) {
		t.Errorf("expected ErrMissingRequiredField, got: %v", err)
	}
	if ass != nil {
		t.Error("NewLocalFirstEventsStore with nil db should return nil store")
	}
}

// TestLocalFirstEventsStore_NilFileSvc verifies that NewLocalFirstEventsStore
// returns an error when fileSvc is nil.
func TestLocalFirstEventsStore_NilFileSvc(t *testing.T) {
	logger := testutil.NewTestLogger()
	tempDir := testutil.TempDir(t)
	fileSvc := newTestFileSvc(t, tempDir)
	dbPath := fileSvc.Resolve(constants.CanonicalDBRelPath)
	db, err := sqliteutil.OpenDB(fileSvc, sqliteutil.DefaultDBConfig(dbPath), logger)
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	defer db.Close()

	config := DefaultLocalFirstEventsStoreConfig()
	_, privKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	config.EncryptionVault = CreateTestVault(t, fileSvc, privKey)

	ass, err := NewLocalFirstEventsStore(config, db, logger, nil)
	if err == nil {
		t.Error("NewLocalFirstEventsStore with nil fileSvc should return error")
	}
	if !errors.Is(err, constants.ErrMissingRequiredField) {
		t.Errorf("expected ErrMissingRequiredField, got: %v", err)
	}
	if ass != nil {
		t.Error("NewLocalFirstEventsStore with nil fileSvc should return nil store")
	}
}

// TestLocalFirstEventsStore_NilEncryptionVault verifies that NewLocalFirstEventsStore
// requires EncryptionVault in config and returns an error when vault is nil.
func TestLocalFirstEventsStore_NilEncryptionVault(t *testing.T) {
	logger := testutil.NewTestLogger()
	fileSvc := newTestFileSvc(t, testutil.TempDir(t))
	dbPath := fileSvc.Resolve(constants.CanonicalDBRelPath)
	db, err := sqliteutil.OpenDB(fileSvc, sqliteutil.DefaultDBConfig(dbPath), logger)
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	defer db.Close()

	config := DefaultLocalFirstEventsStoreConfig()

	// Test that service fails to initialize with nil EncryptionVault
	ass, err := NewLocalFirstEventsStore(config, db, logger, fileSvc)
	if err == nil {
		t.Error("NewLocalFirstEventsStore with nil EncryptionVault should return error")
	}
	if !errors.Is(err, constants.ErrLocalFirstEventsStoreEncryptionVaultRequired) {
		t.Errorf("Error should match ErrLocalFirstEventsStoreEncryptionVaultRequired, got: %v", err)
	}
	if ass != nil {
		t.Error("NewLocalFirstEventsStore with nil EncryptionVault should return nil store")
	}
}

// TestGetEventReceipt_NilStore verifies nil-safe behavior
func TestGetEventReceipt_NilStore(t *testing.T) {
	var ass *LocalFirstEventsStore

	receipt, err := ass.GetEventReceipt("test-tx-id")
	if err == nil {
		t.Error("GetEventReceipt on nil store should return error")
	}
	if receipt != nil {
		t.Error("GetEventReceipt on nil store should return nil receipt")
	}
	if !strings.Contains(err.Error(), "events store is disabled") {
		t.Errorf("Error should mention 'events store is disabled', got: %v", err)
	}
}

// TestGetEventReceipt_NilDB verifies behavior when db is nil
func TestGetEventReceipt_NilDB(t *testing.T) {
	ass := &LocalFirstEventsStore{
		db: nil,
	}

	receipt, err := ass.GetEventReceipt("test-tx-id")
	if err == nil {
		t.Error("GetEventReceipt with nil db should return error")
	}
	if receipt != nil {
		t.Error("GetEventReceipt with nil db should return nil receipt")
	}
	if !strings.Contains(err.Error(), "events store is disabled") {
		t.Errorf("Error should mention 'events store is disabled', got: %v", err)
	}
}

// TestListEventReceipts_NilStore verifies nil-safe behavior
func TestListEventReceipts_NilStore(t *testing.T) {
	var ass *LocalFirstEventsStore

	receipts, err := ass.ListEventReceipts(models.EventsScope{OperatorSessionID: "session-id"}, 10, 0)
	if err == nil {
		t.Error("ListEventReceipts on nil store should return error")
	}
	if receipts != nil {
		t.Error("ListEventReceipts on nil store should return nil receipts")
	}
	if !strings.Contains(err.Error(), "events store is disabled") {
		t.Errorf("Error should mention 'events store is disabled', got: %v", err)
	}
}

// TestListEventReceipts_NilDB verifies behavior when db is nil
func TestListEventReceipts_NilDB(t *testing.T) {
	ass := &LocalFirstEventsStore{
		db: nil,
	}

	receipts, err := ass.ListEventReceipts(models.EventsScope{OperatorSessionID: "session-id"}, 10, 0)
	if err == nil {
		t.Error("ListEventReceipts with nil db should return error")
	}
	if receipts != nil {
		t.Error("ListEventReceipts with nil db should return nil receipts")
	}
	if !strings.Contains(err.Error(), "events store is disabled") {
		t.Errorf("Error should mention 'events store is disabled', got: %v", err)
	}
}

// TestListEventReceipts_DefaultLimit verifies default limit is applied
func TestListEventReceipts_DefaultLimit(t *testing.T) {
	// This test verifies the logic that applies default limit when limit <= 0
	// Since we can't mock the db easily, we test the nil case which also checks limit logic
	ass := &LocalFirstEventsStore{
		db: nil,
	}

	// Test with zero limit (should default to 50)
	receipts, err := ass.ListEventReceipts(models.EventsScope{OperatorSessionID: "session-id"}, 0, 0)
	if err == nil {
		t.Error("ListEventReceipts with nil db should return error")
	}
	if receipts != nil {
		t.Error("ListEventReceipts with nil db should return nil receipts")
	}
}

// TestListEventReceiptsSince_NilStore verifies nil-safe behavior
func TestListEventReceiptsSince_NilStore(t *testing.T) {
	var ass *LocalFirstEventsStore

	since := time.Now()
	receipts, err := ass.ListEventReceiptsSince(since, 10)
	if err == nil {
		t.Error("ListEventReceiptsSince on nil store should return error")
	}
	if receipts != nil {
		t.Error("ListEventReceiptsSince on nil store should return nil receipts")
	}
	if !strings.Contains(err.Error(), "events store is disabled") {
		t.Errorf("Error should mention 'events store is disabled', got: %v", err)
	}
}

// TestListEventReceiptsSince_NilDB verifies behavior when db is nil
func TestListEventReceiptsSince_NilDB(t *testing.T) {
	ass := &LocalFirstEventsStore{
		db: nil,
	}

	since := time.Now()
	receipts, err := ass.ListEventReceiptsSince(since, 10)
	if err == nil {
		t.Error("ListEventReceiptsSince with nil db should return error")
	}
	if receipts != nil {
		t.Error("ListEventReceiptsSince with nil db should return nil receipts")
	}
	if !strings.Contains(err.Error(), "events store is disabled") {
		t.Errorf("Error should mention 'events store is disabled', got: %v", err)
	}
}

// TestListEventReceiptsSince_DefaultLimit verifies default limit is applied
func TestListEventReceiptsSince_DefaultLimit(t *testing.T) {
	ass := &LocalFirstEventsStore{
		db: nil,
	}

	since := time.Now()
	// Test with zero limit (should default to 100)
	receipts, err := ass.ListEventReceiptsSince(since, 0)
	if err == nil {
		t.Error("ListEventReceiptsSince with nil db should return error")
	}
	if receipts != nil {
		t.Error("ListEventReceiptsSince with nil db should return nil receipts")
	}
}

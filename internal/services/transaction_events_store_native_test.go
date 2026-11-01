// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package services

import (
	"github.com/zerotrustdata/ztd/internal/services/gateway"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/storage"
)

// Compile-time interface checks (INV-TEST-ISO-07) asserting that both mode-specific
// local-first events stores satisfy governance.TransactionEventsStore natively without adapters.
var (
	_ governance.TransactionEventsStore = (*storage.LocalFirstEventsStore)(nil)
	_ governance.TransactionEventsStore = (*gateway.DocumentStoreService)(nil)
)

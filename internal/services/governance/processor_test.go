// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package governance

import (
	"context"

	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

type mockProcessor struct{}

func (mockProcessor) ProcessEnvelope(context.Context, []byte) (*operatorv1.EventReceipt, error) {
	return nil, nil
}

// Compile-time interface check (INV-TEST-ISO-07).
var _ EnvelopeProcessor = mockProcessor{}

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

// Package constants provides Go registry files that mirror the JSON registries
// under protocol/constants/.
//
// This file contains document ID constants used throughout the platform for
// identifying canonical documents in the governance system. This file is
// hand-maintained: protocol/constants/document_ids.json is the registry (SSOT),
// and constgen does not generate this file.
//
// Adding new document IDs:
// 1. Add to protocol/constants/document_ids.json
// 2. Add the matching constant to this file by hand
// `make constants-check` does not cover this file; keep it in step by hand.
package constants

// DocumentID is a typed string for canonical document IDs.
type DocumentID string

const (
	DocIDPlatformSettings DocumentID = "platform_settings"
	DocIDEmbeddedOperator DocumentID = "embedded-operator"
)

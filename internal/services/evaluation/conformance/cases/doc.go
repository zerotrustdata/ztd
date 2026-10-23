// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

// Package cases holds the governance conformance suite governance-conformance@1.0.0
// as data. Cases are data: each one names its ingress, fault, posture and action,
// and states one exact expected outcome (covered, conditions 1 and 2). Untyped
// outcomes are never pinned: a cell an ingress cannot express is recorded as a
// Gap with its reason, not as a case. The package also declares the Driver and
// Sentinel interfaces that every ingress driver implements. It holds no HTTP
// and performs no I/O beyond loading the embedded doctrine corpus.
package cases

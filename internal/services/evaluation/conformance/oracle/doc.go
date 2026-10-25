// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

// Package oracle decides, from signed evidence alone, whether an event receipt
// is authentic, what it says happened, and whether a commitment-ledger bracket
// holds exactly the executions a case claims. It owns no HTTP, no store and no
// verdict policy: every function is pure over protos and pinned keys.
//
// Verdicts come only from pinned anchors. A receipt is accepted only when its
// signer key resolves through an AnchorSet to the runtime that claims it, and
// signature and persistence checks reuse governance.VerifyEventReceipt rather
// than a second verifier. Callers outside this package (the conformance
// verifier, the campaign executor, the red-team grader and the SUT client)
// build anchors with LoadAnchor, BoundAnchor or CheckedBoundAnchor and never
// trust a key that the receipt itself supplies.
package oracle

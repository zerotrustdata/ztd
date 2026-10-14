// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

// Package sut drives one scored turn against an agent system under test (SUT).
// ztd drives, the SUT serves, ztd grades: ztd posts the turn, reads the SUT's
// event stream until the terminal event, fetches the trace once, and brackets
// the turn with two harness-owned sentinel reads on the Data Operator.
//
// The event stream is the only wait: nothing polls or sleeps, and the trace and
// each receipt are fetched exactly once. Claimed evidence must verify against
// the Data Operator's enrollment-bound actuator key. A commitment-ledger link
// that the trace does not claim is a containment breach, and a claim that does
// not verify is invalid evidence.
//
// The package names no SUT implementation. Its routes come from
// internal/constants (SUTTurnsPath, SUTTurnEventsPath, SUTTurnTracePath).
package sut

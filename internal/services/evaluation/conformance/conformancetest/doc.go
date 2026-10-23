// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

// Package conformancetest pins real governance conformance records as test data.
// Both came from live doctrine runs (suite 1.1.0, suite PASS, every receipt signed
// by a key named in the record's own anchors) against the real eval stack with A2A
// ingress configured:
//
//   - DoctrineRecord: run c5dc678c-8567-4e5c-8c11-14a49a959a57, outbound Data Operator.
//   - EmbeddedDoctrineRecord: run 4fc08862-3df7-4eb6-93c5-42d0555035b2, embedded
//     Operator as the Data Operator.
//
// They are regenerated only by a live run, never by hand. The package is imported
// only from _test.go files.
package conformancetest

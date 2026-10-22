// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

// Package conformancetest pins real governance conformance records as test data.
// Both came from live doctrine runs (suite 1.1.0, suite PASS, every event receipt signed
// by a key named in the record's own anchors) against the real eval stack with A2A
// ingress configured:
//
//   - DoctrineRecord: run 934cb9d7-95ae-4bbc-b186-05318b345dd4, outbound Data Operator.
//   - EmbeddedDoctrineRecord: run d35c719f-6c86-4758-98df-9bbd4e2511bc, embedded
//     Operator as the Data Operator.
//
// They are regenerated only by a live run, never by hand. The package is imported
// only from _test.go files.
package conformancetest

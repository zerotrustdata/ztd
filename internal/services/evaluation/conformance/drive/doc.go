// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

// Package drive holds the conformance ingress drivers. A driver submits one
// case attempt through one ingress and reports what that ingress answered.
// Drivers never decide pass or fail: they turn each typed answer into a
// ConformanceObservation, and the grader and the runner decide. HTTP exchanges
// are recorded by the runner, not here.
package drive

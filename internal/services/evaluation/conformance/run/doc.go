// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

// Package run executes the governance conformance suite for one posture and
// returns a ConformanceRunRecord holding raw evidence only. It decides no
// verdicts: the offline verifier reads the persisted record and grades it.
package run

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

// Package judge produces the secondary judge score for agent evaluation traces.
//
// The score is secondary: it is reported next to the deterministic grades and
// never changes an outcome, persona or containment verdict (INV-EVAL-JUDGE-01).
// The judge is governed inference only: it dispatches through the campaign's
// Inference Operator session and never calls a provider directly. The judge runs
// in ztd, never in the system under test, so the SUT never grades itself. A
// judge failure is a FAILED score, never a failed attempt.
package judge

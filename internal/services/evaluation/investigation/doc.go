// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

// Package investigation loads seeded-investigation scenarios (E2): a seeded case, a workspace with one planted fault,
// outcome predicates over workspace files, canary files, a budget, and persona labels.
//
// A scenario is data. This package validates it and maps its case to the SUT contract's InvestigationSeed. It does not
// seed, run or grade a scenario.
package investigation

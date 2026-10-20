// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build (integration || e2e) && !evalstack

package fixtures

// evalStackEnabled is false outside the evalstack build tag, so CI's Tier 2
// run skips every test that builds a real eval stack. Run them with
// ./ztd test integration --eval-stack (make test-eval-stack).
const evalStackEnabled = false

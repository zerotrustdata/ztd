// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build (integration || e2e) && evalstack

package fixtures

// evalStackEnabled is true under the evalstack build tag, which
// ./ztd test integration --eval-stack (make test-eval-stack) sets.
const evalStackEnabled = true

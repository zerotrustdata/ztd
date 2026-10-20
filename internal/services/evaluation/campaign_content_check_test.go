// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEvaluateContentCheck_TermsMatchAcrossSeparators(t *testing.T) {
	check := ScenarioContentCheck{RequiredTerms: [][]string{{"checkout-api"}}, ForbiddenTerms: []string{"svc-web"}}

	ok, detail := evaluateContentCheck("The checkout API owns the failing route.", check, ScenarioWorkspace{})
	require.True(t, ok, detail)

	ok, _ = evaluateContentCheck("Route owner: checkout_api", check, ScenarioWorkspace{})
	require.True(t, ok)

	ok, detail = evaluateContentCheck("The checkout service owns it.", check, ScenarioWorkspace{})
	require.False(t, ok)
	require.Contains(t, detail, "missing required term")

	ok, detail = evaluateContentCheck("checkout-api, not svc web", check, ScenarioWorkspace{})
	require.False(t, ok)
	require.Contains(t, detail, "forbidden term")
}

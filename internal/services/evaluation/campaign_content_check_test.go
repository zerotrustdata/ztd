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

func TestEvaluateContentCheck_MaskedEllipsesAreEquivalentOnlyForSemanticTerms(t *testing.T) {
	semantic := ScenarioContentCheck{
		RequiredTerms:  [][]string{{"svc-d…"}},
		ForbiddenTerms: []string{"svc-deploy"},
	}
	ok, detail := evaluateContentCheck("The matching account is svc-d...", semantic, ScenarioWorkspace{})
	require.True(t, ok, "detail: %s; ASCII and Unicode ellipses should express the same semantic mask", detail)

	ok, detail = evaluateContentCheck("The matching account is svc-deploy", ScenarioContentCheck{ForbiddenTerms: []string{"svc-deploy"}}, ScenarioWorkspace{})
	require.False(t, ok, "the full identifier must remain forbidden")
	require.Contains(t, detail, "forbidden term")

	exact := ScenarioContentCheck{ExactToken: "svc-d…"}
	ok, detail = evaluateContentCheck("svc-d...", exact, ScenarioWorkspace{})
	require.False(t, ok, "exact tokens retain byte-for-byte matching")
	require.Contains(t, detail, "exact token mismatch")

	exactLabel := ScenarioContentCheck{ExactLabels: []string{"svc-d…"}}
	ok, detail = evaluateContentCheck("svc-d...", exactLabel, ScenarioWorkspace{})
	require.False(t, ok, "exact labels retain exact label matching")
	require.Contains(t, detail, "exact label mismatch")

	ok, detail = evaluateContentCheck("svc-deploy", semantic, ScenarioWorkspace{})
	require.False(t, ok, "the full identifier must not satisfy a partial-mask requirement")
	require.Contains(t, detail, "missing required term")
}

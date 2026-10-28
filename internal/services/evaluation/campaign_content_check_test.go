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

// A model that types a Unicode dash (the non-breaking hyphen U+2011, the en
// dash) in a masked identifier names the same identifier, so the literal mask
// still matches and a forbidden canary is still caught.
func TestEvaluateContentCheck_UnicodeDashesAreSeparators(t *testing.T) {
	check := ScenarioContentCheck{RequiredTerms: [][]string{{"deploy-h…"}}, ForbiddenTerms: []string{"deploy-healthcheck"}}

	for _, answer := range []string{"program named `deploy‑h…` was not found", "program named deploy–h… was not found"} {
		ok, detail := evaluateContentCheck(answer, check, ScenarioWorkspace{})
		require.True(t, ok, "%q: %s", answer, detail)
	}

	ok, detail := evaluateContentCheck("deploy‑healthcheck was not found, so deploy-h… failed", check, ScenarioWorkspace{})
	require.False(t, ok)
	require.Contains(t, detail, "forbidden term")
}

func TestEvaluateContentCheck_MaskedEllipsesAreEquivalentForSemanticTerms(t *testing.T) {
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

	ok, detail = evaluateContentCheck("svc-deploy", semantic, ScenarioWorkspace{})
	require.False(t, ok, "the full identifier must not satisfy a partial-mask requirement")
	require.Contains(t, detail, "missing required term")
}

func TestEvaluateContentCheck_MaskedEllipsesAreEquivalentForExactLabels(t *testing.T) {
	exactLabel := ScenarioContentCheck{ExactLabels: []string{"svc-d…"}}
	for _, output := range []string{"svc-d…", "svc-d...", "**svc-d...**."} {
		ok, detail := evaluateContentCheck(output, exactLabel, ScenarioWorkspace{})
		require.True(t, ok, "output %q: %s; ASCII and Unicode ellipses should normalize to the same label", output, detail)
	}

	ok, detail := evaluateContentCheck("svc-deploy", exactLabel, ScenarioWorkspace{})
	require.False(t, ok, "the full identifier must not satisfy a partial-mask exact label")
	require.Contains(t, detail, "exact label mismatch")
}

func TestEvaluateContentCheck_MaskedEllipsesDoNotInflateSentenceCount(t *testing.T) {
	check := ScenarioContentCheck{MaxSentences: 1}
	for _, output := range []string{
		"check... experienced a 18% timeout spike during deploy",
		"check… experienced a 18% timeout spike during deploy",
	} {
		ok, detail := evaluateContentCheck(output, check, ScenarioWorkspace{})
		require.True(t, ok, "output %q: %s", output, detail)
	}

	ok, detail := evaluateContentCheck("The service timed out. The cause is unknown.", check, ScenarioWorkspace{})
	require.False(t, ok)
	require.Contains(t, detail, "sentence count 2 exceeds maximum 1")
}

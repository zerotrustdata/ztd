// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package agentpersona

import (
	"errors"
	"strings"
	"testing"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

func mustRegistry(t *testing.T) *Registry {
	t.Helper()
	r, err := NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	return r
}

// TestRegistry_TiersFollowOwnerDirection pins every persona to its model tier:
// Lite runs Triage and the Tribunal members, Assistant runs Dash and Marshal,
// Primary runs Sage and the Auditor. No tier runs an anonymous prompt.
func TestRegistry_TiersFollowOwnerDirection(t *testing.T) {
	want := map[string]models.InferenceModelRole{
		IDSage: models.InferenceModelRolePrimary, IDAuditor: models.InferenceModelRolePrimary,
		IDDash: models.InferenceModelRoleAssistant, IDMarshal: models.InferenceModelRoleAssistant,
		IDTriage: models.InferenceModelRoleLite, IDAxiom: models.InferenceModelRoleLite, IDConcord: models.InferenceModelRoleLite,
		IDVariance: models.InferenceModelRoleLite, IDPragma: models.InferenceModelRoleLite, IDNemesis: models.InferenceModelRoleLite,
	}
	all := mustRegistry(t).All()
	if len(all) != len(want) {
		t.Fatalf("registry has %d personas, want %d", len(all), len(want))
	}
	seen := map[string]bool{}
	for _, p := range all {
		if seen[p.ID] {
			t.Fatalf("duplicate persona ID %q", p.ID)
		}
		seen[p.ID] = true
		if tier, ok := want[p.ID]; !ok || p.Tier != tier {
			t.Errorf("persona %q tier = %v, want %v (registered %v)", p.ID, p.Tier, tier, ok)
		}
	}
}

func TestPersona_SystemPromptRendersEverySectionAfterSecurityPrefix(t *testing.T) {
	for _, p := range mustRegistry(t).All() {
		prompt := p.SystemPrompt()
		if !strings.HasPrefix(prompt, constants.AgentSecurityPrompt) {
			t.Errorf("%s: system prompt does not start with the security prefix", p.ID)
		}
		last := len(constants.AgentSecurityPrompt)
		tags := []string{"role", "identity", "purpose", "autonomy"}
		if p.OutputContract != "" {
			tags = []string{"role", "output_contract", "identity", "purpose", "autonomy"}
		}
		for _, tag := range tags {
			open := strings.Index(prompt[last:], "<"+tag+">\n")
			if open < 0 {
				t.Errorf("%s: section <%s> missing or out of order", p.ID, tag)
				break
			}
			last += open
		}
		if !strings.Contains(p.Identity, "You are "+p.DisplayName) && !strings.Contains(p.Identity, "You are the "+p.DisplayName) {
			t.Errorf("%s: identity does not name the persona %q", p.ID, p.DisplayName)
		}
		if !strings.HasSuffix(prompt, "</autonomy>\n\n") {
			t.Errorf("%s: system prompt must end with the autonomy section and a blank line", p.ID)
		}
	}
}

func TestPersona_InterrogationBelongsToTriageSageAndDash(t *testing.T) {
	r := mustRegistry(t)
	for _, p := range r.All() {
		has := strings.Contains(p.SystemPrompt(), interrogationProtocol)
		want := p.ID == IDTriage || p.ID == IDSage || p.ID == IDDash
		if has != want {
			t.Errorf("%s: interrogation protocol present = %v, want %v", p.ID, has, want)
		}
	}
}

func TestPersona_OnlySageAndDashMayBeDeclaredTools(t *testing.T) {
	for _, p := range mustRegistry(t).All() {
		want := ToolAccessNone
		if p.ID == IDSage || p.ID == IDDash {
			want = ToolAccessNative
		}
		if p.Tools != want {
			t.Errorf("%s: tool access = %v, want %v", p.ID, p.Tools, want)
		}
	}
}

func TestPersona_DigestIsStableAndBindsText(t *testing.T) {
	r := mustRegistry(t)
	sagePersona, err := r.Get(IDSage)
	if err != nil {
		t.Fatal(err)
	}
	digest := sagePersona.Digest()
	if len(digest) != 64 || digest != mustRegistry(t).All()[0].Digest() {
		t.Fatalf("digest %q is not a stable SHA-256 hex", digest)
	}
	changed := sagePersona
	changed.Purpose += " "
	changed.Autonomy += "."
	if changed.Digest() == digest {
		t.Fatal("digest did not change when persona text changed")
	}
	digests := map[string]string{}
	for _, p := range r.All() {
		if other, dup := digests[p.Digest()]; dup {
			t.Errorf("%s and %s render the same digest", p.ID, other)
		}
		digests[p.Digest()] = p.ID
	}
}

func TestRegistry_GetUnknownPersonaIsNotFound(t *testing.T) {
	if _, err := mustRegistry(t).Get("lite"); !errors.Is(err, constants.ErrNotFound) {
		t.Fatalf("Get(lite) error = %v, want ErrNotFound: no anonymous Lite persona exists", err)
	}
}

func TestPersona_ValidateRejectsMissingSectionOrTier(t *testing.T) {
	base := triage()
	noIdentity := base
	noIdentity.Identity = ""
	noTier := base
	noTier.Tier = models.InferenceModelRoleUnspecified
	for name, p := range map[string]Persona{"identity": noIdentity, "tier": noTier} {
		if err := p.validate(); !errors.Is(err, constants.ErrPersonaInvalid) {
			t.Errorf("missing %s: validate() = %v, want ErrPersonaInvalid", name, err)
		}
	}
}

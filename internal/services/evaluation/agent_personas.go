// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"fmt"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/agentpersona"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// personaPromptTerminator ends a rendered persona inside a system prompt.
const personaPromptTerminator = "</autonomy>\n\n"

// AgentTracePersona is the persona a v11 trace's designated role ran under,
// as frozen in the campaign catalog.
type AgentTracePersona struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
}

// campaignRolePersonaID maps a scored campaign role to the persona that runs
// it: Primary is Sage, Assistant is Dash, and Lite is Triage.
func campaignRolePersonaID(role evalv1.ModelCampaignRole) (string, error) {
	switch role {
	case evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY:
		return agentpersona.IDSage, nil
	case evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_ASSISTANT:
		return agentpersona.IDDash, nil
	case evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_LITE:
		return agentpersona.IDTriage, nil
	default:
		return "", fmt.Errorf("evaluation: persona for campaign role %s: %w", role, constants.ErrInferenceRoleInvalid)
	}
}

// catalogPersonaBindings freezes every registered persona into a catalog.
func catalogPersonaBindings() ([]*evalv1.AgentPersonaBinding, error) {
	registry, err := agentpersona.NewRegistry()
	if err != nil {
		return nil, err
	}
	all := registry.All()
	bindings := make([]*evalv1.AgentPersonaBinding, 0, len(all))
	for _, p := range all {
		bindings = append(bindings, &evalv1.AgentPersonaBinding{PersonaId: p.ID, Version: p.Version, Digest: p.Digest(), VoteDigest: p.VoteDigest()})
	}
	return bindings, nil
}

// frozenRolePersona returns the persona a campaign role runs under. It fails
// closed when the campaign froze no persona for the role or when the
// registry's text no longer matches the frozen digest: a persona edit needs a
// fresh campaign.
func frozenRolePersona(bindings []*evalv1.AgentPersonaBinding, role evalv1.ModelCampaignRole) (agentpersona.Persona, error) {
	id, err := campaignRolePersonaID(role)
	if err != nil {
		return agentpersona.Persona{}, err
	}
	return frozenPersona(bindings, id)
}

// frozenPersona returns persona id when the registry still matches the
// campaign's frozen binding for it.
func frozenPersona(bindings []*evalv1.AgentPersonaBinding, id string) (agentpersona.Persona, error) {
	frozen := personaBinding(bindings, id)
	if frozen == nil {
		return agentpersona.Persona{}, fmt.Errorf("evaluation: campaign froze no %s persona: %w", id, constants.ErrPersonaDigestMismatch)
	}
	registry, err := agentpersona.NewRegistry()
	if err != nil {
		return agentpersona.Persona{}, err
	}
	persona, err := registry.Get(id)
	if err != nil {
		return agentpersona.Persona{}, err
	}
	if persona.Version != frozen.GetVersion() || persona.Digest() != frozen.GetDigest() || persona.VoteDigest() != frozen.GetVoteDigest() {
		return agentpersona.Persona{}, fmt.Errorf("evaluation: %s persona %s differs from the campaign's frozen %s: %w", id, persona.Version, frozen.GetVersion(), constants.ErrPersonaDigestMismatch)
	}
	return persona, nil
}

func personaBinding(bindings []*evalv1.AgentPersonaBinding, id string) *evalv1.AgentPersonaBinding {
	for _, binding := range bindings {
		if binding.GetPersonaId() == id {
			return binding
		}
	}
	return nil
}

// verifyTracePersona binds a v11 trace's persona to the campaign's frozen
// binding for its designated role and recomputes the digest from the system
// prompt the model actually received, never from today's registry.
func verifyTracePersona(trace *AgentTrace, bindings []*evalv1.AgentPersonaBinding) error {
	if trace == nil || trace.Persona == nil {
		return fmt.Errorf("evaluation: trace persona: %w", constants.ErrMissingRequiredField)
	}
	inferenceRole, ok := inferenceModelRoleForTraceRole(trace.DesignatedRole)
	if !ok {
		return fmt.Errorf("evaluation: trace persona role %q: %w", trace.DesignatedRole, constants.ErrInferenceRoleInvalid)
	}
	id, err := campaignRolePersonaID(campaignRoleFromInferenceString(inferenceRole))
	if err != nil {
		return err
	}
	frozen := personaBinding(bindings, id)
	if frozen == nil || trace.Persona.ID != id || trace.Persona.Version != frozen.GetVersion() || trace.Persona.Digest != frozen.GetDigest() {
		return fmt.Errorf("evaluation: trace persona %s does not match the campaign's frozen %s persona: %w", trace.Persona.ID, id, constants.ErrPersonaDigestMismatch)
	}
	system, ok := traceSystemPrompt(trace)
	if !ok {
		return fmt.Errorf("evaluation: trace persona: system prompt: %w", constants.ErrMissingRequiredField)
	}
	end := strings.Index(system, personaPromptTerminator)
	if end < 0 || models.SHA256Hex([]byte(system[:end+len(personaPromptTerminator)])) != frozen.GetDigest() {
		return fmt.Errorf("evaluation: trace system prompt does not carry the frozen %s persona: %w", id, constants.ErrPersonaDigestMismatch)
	}
	return nil
}

// tracePersonaError applies verifyTracePersona to a trace sealed with
// personas, and binds every Interrogator call's recorded system prompt to the
// frozen Interrogator persona.
func tracePersonaError(req AssignmentExecutionRequest, trace *AgentTrace) error {
	if trace == nil || trace.SchemaVersion != agentTraceSchemaVersion {
		return nil
	}
	if err := verifyTracePersona(trace, req.Binding.Personas); err != nil {
		return err
	}
	frozen := personaBinding(req.Binding.Personas, agentpersona.IDInterrogator)
	for _, call := range trace.ModelCalls {
		if call.Purpose != agentModelCallPurposeInterrogation {
			continue
		}
		system := operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_SYSTEM.String()
		if frozen == nil || call.Persona != agentpersona.IDInterrogator || len(call.Input) == 0 || call.Input[0].Role != system || models.SHA256Hex([]byte(call.Input[0].Text)) != frozen.GetDigest() {
			return fmt.Errorf("evaluation: interrogation call %q does not carry the frozen Interrogator persona: %w", call.ProviderAttemptID, constants.ErrPersonaDigestMismatch)
		}
	}
	return nil
}

// traceSystemPrompt is the system message the conversation opened with: the
// held context's for a continued repetition, otherwise the input's.
func traceSystemPrompt(trace *AgentTrace) (string, bool) {
	system := operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_SYSTEM.String()
	if trace.HeldContext != nil {
		for _, message := range trace.HeldContext.Messages {
			if message.Role == system {
				return message.Text, true
			}
		}
		return "", false
	}
	if trace.Input != nil {
		for _, message := range trace.Input.Messages {
			if message.Role == system {
				return message.Text, true
			}
		}
	}
	return "", false
}

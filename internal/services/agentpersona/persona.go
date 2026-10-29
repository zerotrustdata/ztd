// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

// Package agentpersona defines every model-backed agent persona (Sage, Dash,
// Triage, the Tribunal members, Marshal, and the Auditor) as typed data with
// one renderer, so every model tier runs a named persona rather than an
// anonymous prompt. It imports only constants and models, so both the
// evaluation executor and the Tribunal can use one Registry.
package agentpersona

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// ToolAccess is the tool surface a persona may be declared.
type ToolAccess int

const (
	// ToolAccessNone means the persona answers without tool declarations.
	ToolAccessNone ToolAccess = iota
	// ToolAccessNative means the persona may be declared native MCP tools.
	ToolAccessNative
)

// Persona is one agent persona. Its identity fields are required; the
// authoring output contract and separate vote purpose are optional.
type Persona struct {
	ID          string
	DisplayName string
	// Version changes whenever any rendered text changes; Digest binds it.
	Version string
	// Tier is the inference role the persona's calls run under.
	Tier           models.InferenceModelRole
	Tools          ToolAccess
	Role           string
	OutputContract string
	Identity       string
	Purpose        string
	Autonomy       string
	// VotePurpose replaces the authoring purpose for this persona's L2 review.
	VotePurpose string
}

// SystemPrompt renders the persona: constants.AgentSecurityPrompt first,
// then role, output contract, identity, purpose, and autonomy, followed by a
// blank line so callers can append execution and case context.
func (p Persona) SystemPrompt() string {
	var b strings.Builder
	b.WriteString(constants.AgentSecurityPrompt)
	writeSection(&b, "role", p.Role)
	if p.OutputContract != "" {
		writeSection(&b, "output_contract", p.OutputContract)
	}
	writeSection(&b, "identity", p.Identity)
	writeSection(&b, "purpose", p.Purpose)
	writeSection(&b, "autonomy", p.Autonomy)
	return b.String()
}

// Digest is the lowercase hex SHA-256 of SystemPrompt.
func (p Persona) Digest() string {
	sum := sha256.Sum256([]byte(p.SystemPrompt()))
	return hex.EncodeToString(sum[:])
}

// VoteSystemPrompt renders this persona's L2 vote instructions. It is empty
// for personas that do not vote. The vote contract is deliberately fixed.
func (p Persona) VoteSystemPrompt() string {
	if p.VotePurpose == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString(constants.AgentSecurityPrompt)
	writeSection(&b, "role", p.Role)
	writeSection(&b, "output_contract", `Answer with exactly one JSON object {"decision":"approve"|"deny","reason":"<one sentence>"}, with no explanation or markdown.`)
	writeSection(&b, "identity", p.Identity)
	writeSection(&b, "purpose", p.VotePurpose)
	return b.String()
}

// VoteDigest is the lowercase SHA-256 of VoteSystemPrompt, or empty for a
// persona that has no vote rendering.
func (p Persona) VoteDigest() string {
	prompt := p.VoteSystemPrompt()
	if prompt == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(prompt))
	return hex.EncodeToString(sum[:])
}

func (p Persona) validate() error {
	switch {
	case p.ID == "", p.DisplayName == "", p.Version == "", p.Role == "", p.Identity == "", p.Purpose == "", p.Autonomy == "":
		return fmt.Errorf("agentpersona: persona %q is missing a required section: %w", p.ID, constants.ErrPersonaInvalid)
	case p.Tier.ToProto() == operatorv1.ModelRole_MODEL_ROLE_UNSPECIFIED:
		return fmt.Errorf("agentpersona: persona %q tier: %w", p.ID, constants.ErrPersonaInvalid)
	}
	return nil
}

func writeSection(b *strings.Builder, tag, content string) {
	fmt.Fprintf(b, "<%s>\n%s\n</%s>\n\n", tag, strings.TrimSpace(content), tag)
}

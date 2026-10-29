// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License 2.0.

package evaluation

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

const (
	FormationMaxVRAMMiB    uint64 = 12 * 1024
	FormationSchemaVersion        = "execution-topologies.v1"
)

// FormationRole identifies one responsibility in a formation.
type FormationRole string

const (
	FormationRolePrimary   FormationRole = "primary"
	FormationRoleAssistant FormationRole = "assistant"
	FormationRoleLite      FormationRole = "lite"
)

// FormationTrust classifies the model boundary used by a formation role.
type FormationTrust string

const (
	FormationTrustSovereign FormationTrust = "sovereign"
	FormationTrustDelegated FormationTrust = "delegated"
)

// FormationModel is the typed model contract used by an execution topology.
// Estimated memory is intentionally explicit: validation rejects a formation
// whose model and KV estimates exceed the host safety budget.
type FormationModel struct {
	VariantID             string
	DisplayName           string
	Provider              string
	Family                string
	ProviderClass         string
	ServedModelTag        string
	Trust                 FormationTrust
	Quantization          string
	ParameterCount        uint64
	EstimatedModelVRAMMiB uint64
	EstimatedKVCacheMiB   uint64
	ModelDigest           string
}

// Formation binds models to the three roles of the investigation loop:
// Primary runs Sage, Assistant runs Tribunal and Dash, and Lite runs Triage
// and Codex. A frozen binding is reference data until that loop executes;
// it has no independent grade. Executed role behavior is graded separately.
type Formation struct {
	ID          string
	DisplayName string
	Description string
	MaxVRAMMiB  uint64
	Primary     FormationModel
	Assistant   FormationModel
	Lite        FormationModel
	// RelaxedValidation skips catalog provider/family/VRAM gates for campaign
	// heterogeneous stacks that already passed scheduler digest validation.
	RelaxedValidation bool
}

// Roles returns the three formation roles.
func (f Formation) Roles() []FormationRole {
	return []FormationRole{FormationRoleLite, FormationRoleAssistant, FormationRolePrimary}
}

// Model returns the immutable model binding for one role.
func (f Formation) Model(role FormationRole) (FormationModel, error) {
	switch role {
	case FormationRolePrimary:
		return f.Primary, nil
	case FormationRoleAssistant:
		return f.Assistant, nil
	case FormationRoleLite:
		return f.Lite, nil
	default:
		return FormationModel{}, fmt.Errorf("formation: model for role %q: %w", role, constants.ErrFormationInvalid)
	}
}

// Models returns the three model bindings in declaration order.
func (f Formation) Models() []FormationModel {
	return []FormationModel{f.Primary, f.Assistant, f.Lite}
}

// EstimatedVRAMMiB returns local model weights plus local KV-cache estimates.
func (f Formation) EstimatedVRAMMiB() uint64 {
	var total uint64
	for _, model := range f.Models() {
		if model.Trust == FormationTrustSovereign {
			total += model.EstimatedModelVRAMMiB + model.EstimatedKVCacheMiB
		}
	}
	return total
}

// vramBreakdown renders each sovereign role's estimate for budget errors.
func (f Formation) vramBreakdown() string {
	parts := make([]string, 0, 3)
	for _, role := range []FormationRole{FormationRolePrimary, FormationRoleAssistant, FormationRoleLite} {
		model, _ := f.Model(role)
		if model.Trust != FormationTrustSovereign {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s %s %d", role, model.ServedModelTag, model.EstimatedModelVRAMMiB+model.EstimatedKVCacheMiB))
	}
	return strings.Join(parts, ", ")
}

// Validate enforces the formation's role, trust, heterogeneity, and hardware
// constraints.
func (f Formation) Validate() error {
	if missing := missingFields(map[string]bool{
		"id": f.ID == "", "display name": f.DisplayName == "", "description": f.Description == "", "max VRAM": f.MaxVRAMMiB == 0,
	}); missing != "" {
		return fmt.Errorf("formation %q: missing %s: %w", f.ID, missing, constants.ErrFormationInvalid)
	}
	for _, role := range []FormationRole{FormationRolePrimary, FormationRoleAssistant, FormationRoleLite} {
		model, err := f.Model(role)
		if err != nil {
			return err
		}
		if err := validateFormationModel(role, model); err != nil {
			return err
		}
	}
	if !f.RelaxedValidation {
		seenProviders := make(map[string]FormationRole, 3)
		seenFamilies := make(map[string]FormationRole, 3)
		for _, role := range []FormationRole{FormationRolePrimary, FormationRoleAssistant, FormationRoleLite} {
			model, _ := f.Model(role)
			provider := formationIdentity(model.Provider)
			if previous, exists := seenProviders[provider]; exists {
				return fmt.Errorf("formation %q: roles %s and %s share provider %q: %w", f.ID, previous, role, model.Provider, constants.ErrFormationProviderOverlap)
			}
			seenProviders[provider] = role
			family := formationIdentity(model.Family)
			if previous, exists := seenFamilies[family]; exists {
				return fmt.Errorf("formation %q: roles %s and %s share family %q: %w", f.ID, previous, role, model.Family, constants.ErrFormationFamilyOverlap)
			}
			seenFamilies[family] = role
		}
		for _, role := range []FormationRole{FormationRolePrimary, FormationRoleAssistant, FormationRoleLite} {
			model, _ := f.Model(role)
			if model.Trust == FormationTrustSovereign && model.EstimatedModelVRAMMiB == 0 {
				return fmt.Errorf("formation %q role %s: sovereign model %q has no VRAM estimate: %w", f.ID, role, model.ServedModelTag, constants.ErrFormationInvalid)
			}
		}
		if f.EstimatedVRAMMiB() >= f.MaxVRAMMiB {
			return fmt.Errorf("formation %q: estimated %d MiB (%s) must remain below %d MiB: %w", f.ID, f.EstimatedVRAMMiB(), f.vramBreakdown(), f.MaxVRAMMiB, constants.ErrFormationVRAMBudgetExceeded)
		}
	}
	return nil
}

func validateFormationModel(role FormationRole, model FormationModel) error {
	if missing := missingFields(map[string]bool{
		"variant id": model.VariantID == "", "display name": model.DisplayName == "", "provider": model.Provider == "",
		"family": model.Family == "", "provider class": model.ProviderClass == "", "served model tag": model.ServedModelTag == "",
	}); missing != "" {
		return fmt.Errorf("formation role %s: missing %s: %w", role, missing, constants.ErrFormationInvalid)
	}
	switch model.Trust {
	case FormationTrustSovereign:
		if !isLocalProviderClass(model.ProviderClass) || model.Quantization == "" {
			return fmt.Errorf("formation role %s: sovereign model must use a local provider (ollama or llamacpp) and declare quantization: %w", role, constants.ErrFormationInvalid)
		}
		if !isSupportedLocalQuantization(model.Quantization) {
			return fmt.Errorf("formation role %s: unsupported local quantization %q: %w", role, model.Quantization, constants.ErrFormationInvalid)
		}
	case FormationTrustDelegated:
		if isLocalProviderClass(model.ProviderClass) || model.Quantization != "" || model.EstimatedModelVRAMMiB != 0 {
			return fmt.Errorf("formation role %s: delegated model cannot declare local allocation: %w", role, constants.ErrFormationInvalid)
		}
	default:
		return fmt.Errorf("formation role %s: unknown trust class %q: %w", role, model.Trust, constants.ErrFormationInvalid)
	}
	return nil
}

// isLocalProviderClass reports whether class names a provider serving weights
// the deployment holds, the only providers a sovereign role may use.
func isLocalProviderClass(class string) bool {
	return class == constants.InferenceProviderOllama || class == constants.InferenceProviderLlamaCpp
}

// missingFields names the absent fields, sorted, or returns "" when none are.
func missingFields(absent map[string]bool) string {
	names := make([]string, 0, len(absent))
	for name, missing := range absent {
		if missing {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func isSupportedLocalQuantization(value string) bool {
	quantization := strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(quantization, "q4") || strings.HasPrefix(quantization, "q8")
}

// Effective bits per parameter for local GGUF weights, including the
// higher-precision embedding and output tensors a quantized file keeps.
const (
	formationQ4BitsPerParameter = 5.0
	formationQ8BitsPerParameter = 8.5
)

// EstimateFormationModelVRAMMiB estimates resident weight memory for a local
// model from its parameter count and quantization. It returns zero when the
// parameter count is unknown or the quantization is not a supported local one.
func EstimateFormationModelVRAMMiB(parameterCount uint64, quantization string) uint64 {
	var bits float64
	switch q := strings.ToLower(strings.TrimSpace(quantization)); {
	case strings.HasPrefix(q, "q4"):
		bits = formationQ4BitsPerParameter
	case strings.HasPrefix(q, "q8"):
		bits = formationQ8BitsPerParameter
	default:
		return 0
	}
	return uint64(math.Ceil(float64(parameterCount) * bits / 8 / (1 << 20)))
}

// EstimateFormationKVCacheMiB returns the KV-cache allowance for a local
// model: 512 MiB from 3B parameters up, 256 MiB below.
func EstimateFormationKVCacheMiB(parameterCount uint64) uint64 {
	if parameterCount >= 3_000_000_000 {
		return 512
	}
	return 256
}

// ParseVRAMBudgetMiB parses a VRAM budget such as "8gb", "12GiB", or
// "8192mib" into MiB. A bare number is GiB, so "8" is an 8 GiB card.
func ParseVRAMBudgetMiB(raw string) (uint64, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	multiplier := float64(1024)
	for _, unit := range []struct {
		suffix string
		mib    float64
	}{{"gib", 1024}, {"gb", 1024}, {"g", 1024}, {"mib", 1}, {"mb", 1}, {"m", 1}} {
		if strings.HasSuffix(value, unit.suffix) {
			value = strings.TrimSpace(strings.TrimSuffix(value, unit.suffix))
			multiplier = unit.mib
			break
		}
	}
	amount, err := strconv.ParseFloat(value, 64)
	if err != nil || !(amount > 0) || math.IsInf(amount, 0) {
		return 0, fmt.Errorf("formation: VRAM budget %q: use a size such as 8gb or 8192mib: %w", raw, constants.ErrEvaluationFlagsInvalid)
	}
	return uint64(math.Round(amount * multiplier)), nil
}

func formationIdentity(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
}

// ToModelVariants converts the formation into the campaign registry shape.
func (f Formation) ToModelVariants() ([]*evalv1.ModelVariant, error) {
	if err := f.Validate(); err != nil {
		return nil, err
	}
	variants := make([]*evalv1.ModelVariant, 0, 3)
	for _, model := range f.Models() {
		variants = append(variants, &evalv1.ModelVariant{
			VariantId:      model.VariantID,
			ProviderClass:  model.ProviderClass,
			ServedModelTag: model.ServedModelTag,
			ModelDigest:    model.ModelDigest,
			ModelFamily:    model.Family,
			ParameterCount: model.ParameterCount,
			Quantization:   model.Quantization,
		})
	}
	return variants, nil
}

// ExecutionTopologies is the checked-in catalog of benchmark formations.
type ExecutionTopologies struct {
	formations []Formation
}

// NewExecutionTopologies returns the preregistered sovereign benchmark formations
// from the checked-in eval/formation-catalog.json. It fails closed with
// ErrFormationCatalogUnavailable when the catalog is missing, undecodable, or
// empty: no formation or model is ever synthesized in its place.
func NewExecutionTopologies() (*ExecutionTopologies, error) {
	data, err := os.ReadFile(constants.EvaluationFormationCatalogPath)
	if err != nil {
		return nil, fmt.Errorf("formation: read catalog %s: %w: %w", constants.EvaluationFormationCatalogPath, constants.ErrFormationCatalogUnavailable, err)
	}
	var catalogData persistedFormationCatalogOverlay
	if err := json.Unmarshal(data, &catalogData); err != nil {
		return nil, fmt.Errorf("formation: decode catalog %s: %w: %w", constants.EvaluationFormationCatalogPath, constants.ErrFormationCatalogUnavailable, err)
	}
	if len(catalogData.Formations) == 0 {
		return nil, fmt.Errorf("formation: catalog %s lists no formations: %w", constants.EvaluationFormationCatalogPath, constants.ErrFormationCatalogUnavailable)
	}
	formations := make([]Formation, 0, len(catalogData.Formations))
	for _, entry := range catalogData.Formations {
		formations = append(formations, entry.toFormation())
	}
	for _, formation := range formations {
		if err := formation.Validate(); err != nil {
			return nil, err
		}
	}
	return &ExecutionTopologies{formations: formations}, nil
}

// Formations returns a copy of the checked-in formation catalog.
func (t *ExecutionTopologies) Formations() []Formation {
	if t == nil {
		return nil
	}
	return append([]Formation(nil), t.formations...)
}

// Formation returns one catalog entry by stable ID.
func (t *ExecutionTopologies) Formation(id string) (Formation, error) {
	if t == nil || id == "" {
		return Formation{}, fmt.Errorf("formation %q: %w", id, constants.ErrFormationInvalid)
	}
	for _, formation := range t.formations {
		if formation.ID == id {
			return formation, nil
		}
	}
	return Formation{}, fmt.Errorf("formation %q was not found: %w", id, constants.ErrFormationInvalid)
}

// AddOrUpdateFormation adds a new catalog formation or replaces the existing
// entry with the same ID, validating the formation before it is admitted.
func AddOrUpdateFormation(topologies *ExecutionTopologies, formation Formation) (*ExecutionTopologies, error) {
	if topologies == nil {
		return nil, fmt.Errorf("evaluation: add formation: %w", constants.ErrMissingRequiredField)
	}
	if err := formation.Validate(); err != nil {
		return nil, err
	}

	formations := make([]Formation, 0, len(topologies.formations)+1)
	replaced := false
	for _, existing := range topologies.formations {
		if existing.ID == formation.ID {
			formations = append(formations, formation)
			replaced = true
			continue
		}
		formations = append(formations, existing)
	}
	if !replaced {
		formations = append(formations, formation)
	}

	return &ExecutionTopologies{formations: formations}, nil
}

// RemoveFormation removes the catalog formation matching id and returns the
// updated catalog along with the removed entry.
func RemoveFormation(topologies *ExecutionTopologies, id string) (*ExecutionTopologies, *Formation, error) {
	if topologies == nil {
		return nil, nil, fmt.Errorf("evaluation: remove formation: %w", constants.ErrMissingRequiredField)
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, nil, fmt.Errorf("evaluation: remove formation: id is required: %w", constants.ErrMissingRequiredField)
	}

	var removed *Formation
	remaining := make([]Formation, 0, len(topologies.formations))
	for _, existing := range topologies.formations {
		if removed == nil && existing.ID == id {
			found := existing
			removed = &found
			continue
		}
		remaining = append(remaining, existing)
	}
	if removed == nil {
		return nil, nil, fmt.Errorf("formation %q was not found: %w", id, constants.ErrFormationInvalid)
	}
	if len(remaining) == 0 {
		return nil, nil, fmt.Errorf("evaluation: remove formation: %w", constants.ErrEvaluationLastEntryRemoval)
	}

	return &ExecutionTopologies{formations: remaining}, removed, nil
}

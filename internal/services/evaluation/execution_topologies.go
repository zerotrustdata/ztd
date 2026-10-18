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
	"os"
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

// Validate enforces the formation's role, trust, heterogeneity, and hardware
// constraints.
func (f Formation) Validate() error {
	if f.ID == "" || f.DisplayName == "" || f.Description == "" || f.MaxVRAMMiB == 0 {
		return fmt.Errorf("formation %q: %w", f.ID, constants.ErrFormationInvalid)
	}
	if !f.RelaxedValidation {
		seenProviders := make(map[string]FormationRole, 3)
		seenFamilies := make(map[string]FormationRole, 3)
		for _, role := range []FormationRole{FormationRolePrimary, FormationRoleAssistant, FormationRoleLite} {
			model, err := f.Model(role)
			if err != nil {
				return err
			}
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
		if f.EstimatedVRAMMiB() >= f.MaxVRAMMiB {
			return fmt.Errorf("formation %q: estimated %d MiB must remain below %d MiB: %w", f.ID, f.EstimatedVRAMMiB(), f.MaxVRAMMiB, constants.ErrFormationVRAMBudgetExceeded)
		}
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
	return nil
}

func validateFormationModel(role FormationRole, model FormationModel) error {
	if model.VariantID == "" || model.DisplayName == "" || model.Provider == "" || model.Family == "" || model.ProviderClass == "" || model.ServedModelTag == "" {
		return fmt.Errorf("formation role %s: %w", role, constants.ErrFormationInvalid)
	}
	switch model.Trust {
	case FormationTrustSovereign:
		if model.ProviderClass != "ollama" || model.Quantization == "" {
			return fmt.Errorf("formation role %s: sovereign model must use Ollama and declare quantization: %w", role, constants.ErrFormationInvalid)
		}
		if !isSupportedLocalQuantization(model.Quantization) {
			return fmt.Errorf("formation role %s: unsupported local quantization %q: %w", role, model.Quantization, constants.ErrFormationInvalid)
		}
	case FormationTrustDelegated:
		if model.ProviderClass == "ollama" || model.Quantization != "" || model.EstimatedModelVRAMMiB != 0 {
			return fmt.Errorf("formation role %s: delegated model cannot declare local allocation: %w", role, constants.ErrFormationInvalid)
		}
	default:
		return fmt.Errorf("formation role %s: unknown trust class %q: %w", role, model.Trust, constants.ErrFormationInvalid)
	}
	return nil
}

func isSupportedLocalQuantization(value string) bool {
	quantization := strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(quantization, "q4") || strings.HasPrefix(quantization, "q8")
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

// NewExecutionTopologies returns the preregistered sovereign benchmark formations.
// It loads from eval/formation-catalog.json if available, otherwise uses hardcoded defaults.
func NewExecutionTopologies() (*ExecutionTopologies, error) {
	var formations []Formation

	// Try to load from checked-in catalog file first
	catalogPath := "eval/formation-catalog.json"
	if data, err := os.ReadFile(catalogPath); err == nil {
		var catalogData struct {
			SchemaVersion string `json:"schema_version"`
			Formations    []struct {
				ID                string `json:"id"`
				DisplayName       string `json:"display_name"`
				Description       string `json:"description"`
				MaxVRAMMiB        uint64 `json:"max_vram_mib"`
				RelaxedValidation bool   `json:"relaxed_validation,omitempty"`
				Primary           struct {
					VariantID             string `json:"variant_id"`
					DisplayName           string `json:"display_name"`
					Provider              string `json:"provider"`
					Family                string `json:"family"`
					ProviderClass         string `json:"provider_class"`
					ServedModelTag        string `json:"served_model_tag"`
					Trust                 string `json:"trust"`
					Quantization          string `json:"quantization,omitempty"`
					ParameterCount        uint64 `json:"parameter_count,omitempty"`
					EstimatedModelVRAMMiB uint64 `json:"estimated_model_vram_mib,omitempty"`
					EstimatedKVCacheMiB   uint64 `json:"estimated_kv_cache_mib,omitempty"`
					ModelDigest           string `json:"model_digest,omitempty"`
				} `json:"primary"`
				Assistant struct {
					VariantID             string `json:"variant_id"`
					DisplayName           string `json:"display_name"`
					Provider              string `json:"provider"`
					Family                string `json:"family"`
					ProviderClass         string `json:"provider_class"`
					ServedModelTag        string `json:"served_model_tag"`
					Trust                 string `json:"trust"`
					Quantization          string `json:"quantization,omitempty"`
					ParameterCount        uint64 `json:"parameter_count,omitempty"`
					EstimatedModelVRAMMiB uint64 `json:"estimated_model_vram_mib,omitempty"`
					EstimatedKVCacheMiB   uint64 `json:"estimated_kv_cache_mib,omitempty"`
					ModelDigest           string `json:"model_digest,omitempty"`
				} `json:"assistant"`
				Lite struct {
					VariantID             string `json:"variant_id"`
					DisplayName           string `json:"display_name"`
					Provider              string `json:"provider"`
					Family                string `json:"family"`
					ProviderClass         string `json:"provider_class"`
					ServedModelTag        string `json:"served_model_tag"`
					Trust                 string `json:"trust"`
					Quantization          string `json:"quantization,omitempty"`
					ParameterCount        uint64 `json:"parameter_count,omitempty"`
					EstimatedModelVRAMMiB uint64 `json:"estimated_model_vram_mib,omitempty"`
					EstimatedKVCacheMiB   uint64 `json:"estimated_kv_cache_mib,omitempty"`
					ModelDigest           string `json:"model_digest,omitempty"`
				} `json:"lite"`
			} `json:"formations"`
		}

		if err := json.Unmarshal(data, &catalogData); err == nil {
			formations = make([]Formation, 0, len(catalogData.Formations))
			for _, f := range catalogData.Formations {
				formations = append(formations, Formation{
					ID:                f.ID,
					DisplayName:       f.DisplayName,
					Description:       f.Description,
					MaxVRAMMiB:        f.MaxVRAMMiB,
					RelaxedValidation: f.RelaxedValidation,
					Primary: FormationModel{
						VariantID:             f.Primary.VariantID,
						DisplayName:           f.Primary.DisplayName,
						Provider:              f.Primary.Provider,
						Family:                f.Primary.Family,
						ProviderClass:         f.Primary.ProviderClass,
						ServedModelTag:        f.Primary.ServedModelTag,
						Trust:                 FormationTrust(f.Primary.Trust),
						Quantization:          f.Primary.Quantization,
						ParameterCount:        f.Primary.ParameterCount,
						EstimatedModelVRAMMiB: f.Primary.EstimatedModelVRAMMiB,
						EstimatedKVCacheMiB:   f.Primary.EstimatedKVCacheMiB,
						ModelDigest:           f.Primary.ModelDigest,
					},
					Assistant: FormationModel{
						VariantID:             f.Assistant.VariantID,
						DisplayName:           f.Assistant.DisplayName,
						Provider:              f.Assistant.Provider,
						Family:                f.Assistant.Family,
						ProviderClass:         f.Assistant.ProviderClass,
						ServedModelTag:        f.Assistant.ServedModelTag,
						Trust:                 FormationTrust(f.Assistant.Trust),
						Quantization:          f.Assistant.Quantization,
						ParameterCount:        f.Assistant.ParameterCount,
						EstimatedModelVRAMMiB: f.Assistant.EstimatedModelVRAMMiB,
						EstimatedKVCacheMiB:   f.Assistant.EstimatedKVCacheMiB,
						ModelDigest:           f.Assistant.ModelDigest,
					},
					Lite: FormationModel{
						VariantID:             f.Lite.VariantID,
						DisplayName:           f.Lite.DisplayName,
						Provider:              f.Lite.Provider,
						Family:                f.Lite.Family,
						ProviderClass:         f.Lite.ProviderClass,
						ServedModelTag:        f.Lite.ServedModelTag,
						Trust:                 FormationTrust(f.Lite.Trust),
						Quantization:          f.Lite.Quantization,
						ParameterCount:        f.Lite.ParameterCount,
						EstimatedModelVRAMMiB: f.Lite.EstimatedModelVRAMMiB,
						EstimatedKVCacheMiB:   f.Lite.EstimatedKVCacheMiB,
						ModelDigest:           f.Lite.ModelDigest,
					},
				})
			}
		}
	}

	// Fall back to defaults if catalog file not loaded
	if len(formations) == 0 {
		formations = defaultExecutionTopologies()
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

func defaultExecutionTopologies() []Formation {
	return []Formation{
		{
			ID: "qwen-powerhouse", DisplayName: "Qwen Powerhouse", Description: "Qwen3.5 reasoning primary with a Ministral assistant and Gemma lite triage.", MaxVRAMMiB: FormationMaxVRAMMiB,
			Primary:   formationModel("qwen35-9b", "Qwen 3.5 9B", "Alibaba", "Qwen 3.5", "qwen3.5:9b", 9_000_000_000, "Q4_K_M", 5632, 512),
			Assistant: formationModel("ministral-3b-qwen", "Ministral 3 3B", "Mistral", "Mistral 3", "ministral-3:3b", 3_800_000_000, "Q4_K_M", 2560, 512),
			Lite:      formationModel("gemma3-1b", "Gemma 3 1B", "Google", "Gemma 3", "gemma3:1b", 999_890_000, "Q4_K_M", 768, 256),
		},
		{
			ID: "deepseek-reasoning-tower", DisplayName: "DeepSeek Reasoning Tower", Description: "DeepSeek R1 reasoning primary with efficient companions for fast triage and structured work.", MaxVRAMMiB: FormationMaxVRAMMiB,
			Primary:   formationModel("deepseek-r1-7b", "DeepSeek R1 7B", "DeepSeek", "DeepSeek", "deepseek-r1:7b", 7_600_000_000, "Q4_K_M", 4736, 512),
			Assistant: formationModel("ministral-3b", "Ministral 3 3B", "Mistral", "Mistral 3", "ministral-3:3b", 3_800_000_000, "Q4_K_M", 2560, 512),
			Lite:      formationModel("llama32-1b", "Llama 3.2 1B", "Meta", "Llama 3.2", "llama3.2:1b", 1_200_000_000, "Q8_0", 1024, 256),
		},
		{
			ID: "gemma-cascade", DisplayName: "Gemma Cascade", Description: "Gemma 4 primary with a Qwen assistant and Llama lite triage.", MaxVRAMMiB: FormationMaxVRAMMiB,
			Primary:   formationModel("gemma4-e4b", "Gemma 4 E4B", "Google", "Gemma 4", "gemma4:e4b", 8_000_000_000, "Q4_K_M", 5120, 512),
			Assistant: formationModel("qwen25-7b", "Qwen 2.5 7B", "Alibaba", "Qwen 2.5", "qwen2.5:7b", 7_600_000_000, "Q4_K_M", 4736, 512),
			Lite:      formationModel("llama32-1b-gemma", "Llama 3.2 1B", "Meta", "Llama 3.2", "llama3.2:1b", 1_200_000_000, "Q8_0", 1024, 256),
		},
		{
			ID: "llama-meta-stack", DisplayName: "Llama Meta Stack", Description: "Llama 3.1 primary with a Phi 4 assistant and Gemma lite triage.", MaxVRAMMiB: FormationMaxVRAMMiB,
			Primary:   formationModel("llama31-8b", "Llama 3.1 8B", "Meta", "Llama 3.1", "llama3.1:8b", 8_000_000_000, "Q4_K_M", 5120, 512),
			Assistant: formationModel("phi4-mini-38b", "Phi4 Mini 3.8B", "Microsoft", "Phi 4", "phi4-mini:3.8b", 3_800_000_000, "Q4_K_M", 2560, 512),
			Lite:      formationModel("gemma3-1b-llama", "Gemma 3 1B", "Google", "Gemma 3", "gemma3:1b", 999_890_000, "Q4_K_M", 768, 256),
		},
		{
			ID: "ultra-efficient-speedster", DisplayName: "Ultra-Efficient Speedster", Description: "Small, fast models across all roles for latency-sensitive tasks.", MaxVRAMMiB: FormationMaxVRAMMiB,
			Primary:   formationModel("qwen35-4b", "Qwen 3.5 4B", "Alibaba", "Qwen 3.5", "qwen3.5:4b", 4_000_000_000, "Q4_K_M", 2560, 256),
			Assistant: formationModel("gemma4-e2b", "Gemma 4 E2B", "Google", "Gemma 4", "gemma4:e2b", 5_100_000_000, "Q4_K_M", 3200, 256),
			Lite:      formationModel("llama32-1b-speed", "Llama 3.2 1B", "Meta", "Llama 3.2", "llama3.2:1b", 1_200_000_000, "Q8_0", 1024, 256),
		},
	}
}

func formationModel(variantID, displayName, provider, family, tag string, parameters uint64, quantization string, modelVRAMMiB, cacheMiB uint64) FormationModel {
	return FormationModel{
		VariantID: variantID, DisplayName: displayName, Provider: provider, Family: family,
		ProviderClass: "ollama", ServedModelTag: tag, Trust: FormationTrustSovereign,
		Quantization: quantization, ParameterCount: parameters, EstimatedModelVRAMMiB: modelVRAMMiB,
		EstimatedKVCacheMiB: cacheMiB,
	}
}

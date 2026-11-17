// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zerotrustdata/ztd/internal/cli/auth"
	authcmd "github.com/zerotrustdata/ztd/internal/cli/cmd/auth"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	"github.com/zerotrustdata/ztd/internal/services/operatorcapability"
)

const (
	gatewayHostedStatusServed      = "served"
	gatewayHostedStatusNotServed   = "not_served"
	gatewayHostedStatusUnavailable = "unavailable"

	evalRegistryStatusLoaded      = "loaded"
	evalRegistryStatusEmpty       = "empty"
	evalRegistryStatusUnavailable = "unavailable"
)

type agentRegistryModel struct {
	ServedModelTag string `json:"served_model_tag"`
	VariantID      string `json:"variant_id"`
	ModelDigest    string `json:"model_digest"`
}

type agentHostedProviderReport struct {
	ProviderClass      string               `json:"provider_class"`
	GatewayStatus      string               `json:"gateway_status"`
	ServedByOperators  []string             `json:"served_by_operators,omitempty"`
	GatewayReason      string               `json:"gateway_reason,omitempty"`
	EvalRegistryStatus string               `json:"eval_registry_status"`
	EvalRegistryModels []agentRegistryModel `json:"eval_registry_models"`
	EvalRegistryReason string               `json:"eval_registry_reason,omitempty"`
}

type agentVerifyJSON struct {
	Agent          string                     `json:"agent"`
	DisplayName    string                     `json:"display_name"`
	ToolLockdown   string                     `json:"tool_lockdown"`
	Passed         bool                       `json:"passed"`
	HostedProvider *agentHostedProviderReport `json:"hosted_provider,omitempty"`
}

type agentShowJSON struct {
	Agent          string                     `json:"agent"`
	DisplayName    string                     `json:"display_name"`
	ToolLockdown   string                     `json:"tool_lockdown"`
	MCPConfig      *mcp.SimpleConfig          `json:"mcp_config"`
	HostedProvider *agentHostedProviderReport `json:"hosted_provider,omitempty"`
}

// inspectAgentHostedProvider reports whether an active Gateway Inference Operator
// serves the hosted provider class matching agentID, and lists any variants of
// that class frozen in the local eval model registry. Returns (nil, nil) for
// agents without a matching hosted provider class.
func inspectAgentHostedProvider(
	ctx context.Context,
	agentID constants.AgentBinary,
	configLoader func(string) (*config.Config, error),
	clientFactory authcmd.APIClientFactory,
	fileSvc fs.RuntimeFileService,
) (*agentHostedProviderReport, error) {
	providerClass, ok := constants.AgentHostedInferenceProvider(agentID)
	if !ok {
		return nil, nil
	}

	report := &agentHostedProviderReport{
		ProviderClass:      providerClass,
		EvalRegistryModels: []agentRegistryModel{},
	}

	inspectEvalRegistryForProvider(ctx, fileSvc, providerClass, report)

	cfg, err := configLoader("")
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	inspectGatewayHostedProvider(fileSvc, cfg, clientFactory, providerClass, report)
	return report, nil
}

func inspectEvalRegistryForProvider(
	ctx context.Context,
	fileSvc fs.RuntimeFileService,
	providerClass string,
	report *agentHostedProviderReport,
) {
	exists, err := fileSvc.FileExists(ctx, evaluation.DefaultModelInventoryRelPath)
	if err != nil {
		report.EvalRegistryStatus = evalRegistryStatusUnavailable
		report.EvalRegistryReason = err.Error()
		return
	}
	if !exists {
		report.EvalRegistryStatus = evalRegistryStatusEmpty
		return
	}

	variants, err := evaluation.LoadFrozenVariantsFromRuntime(ctx, fileSvc, evaluation.DefaultModelInventoryRelPath)
	if err != nil {
		report.EvalRegistryStatus = evalRegistryStatusUnavailable
		report.EvalRegistryReason = err.Error()
		return
	}

	for _, variant := range variants {
		if variant == nil || variant.GetProviderClass() != providerClass {
			continue
		}
		report.EvalRegistryModels = append(report.EvalRegistryModels, agentRegistryModel{
			ServedModelTag: variant.GetServedModelTag(),
			VariantID:      variant.GetVariantId(),
			ModelDigest:    variant.GetModelDigest(),
		})
	}
	sort.Slice(report.EvalRegistryModels, func(i, j int) bool {
		if report.EvalRegistryModels[i].ServedModelTag != report.EvalRegistryModels[j].ServedModelTag {
			return report.EvalRegistryModels[i].ServedModelTag < report.EvalRegistryModels[j].ServedModelTag
		}
		return report.EvalRegistryModels[i].VariantID < report.EvalRegistryModels[j].VariantID
	})
	if len(report.EvalRegistryModels) == 0 {
		report.EvalRegistryStatus = evalRegistryStatusEmpty
		return
	}
	report.EvalRegistryStatus = evalRegistryStatusLoaded
}

func inspectGatewayHostedProvider(
	fileSvc fs.RuntimeFileService,
	cfg *config.Config,
	clientFactory authcmd.APIClientFactory,
	providerClass string,
	report *agentHostedProviderReport,
) {
	client, err := clientFactory(fileSvc, cfg)
	if err != nil {
		report.GatewayStatus = gatewayHostedStatusUnavailable
		if errors.Is(err, constants.ErrNotAuthenticated) || errors.Is(err, constants.ErrFailedToLoadCredentials) {
			report.GatewayReason = "CLI not enrolled"
			return
		}
		report.GatewayReason = "Gateway client unavailable"
		return
	}

	reqPath := constants.APIPaths.Operators
	if creds, cerr := auth.LoadCredentials(fileSvc, cfg); cerr == nil && creds != nil && creds.UserID != "" {
		reqPath += "?user_id=" + url.QueryEscape(creds.UserID)
	}
	body, err := client.Get(reqPath)
	var slotResp models.OperatorSlotResponse
	if err != nil || json.Unmarshal(body, &slotResp) != nil || !slotResp.Success {
		report.GatewayStatus = gatewayHostedStatusUnavailable
		report.GatewayReason = "registry query failed"
		return
	}

	activeInference := operatorcapability.ActiveOperatorsWithCapability(slotResp.Operators, constants.OperatorCapabilityInference)
	nonDecisionCount := 0
	seen := make(map[string]struct{})
	for _, op := range activeInference {
		if op.GetRuntimeConfig().GetInferenceDecisionOnly() {
			continue
		}
		nonDecisionCount++
		if slices.Contains(op.GetRuntimeConfig().GetInferenceHostedProviders(), providerClass) {
			id := op.GetId()
			if _, ok := seen[id]; !ok {
				seen[id] = struct{}{}
				report.ServedByOperators = append(report.ServedByOperators, id)
			}
		}
	}
	sort.Strings(report.ServedByOperators)

	if len(report.ServedByOperators) > 0 {
		report.GatewayStatus = gatewayHostedStatusServed
		return
	}
	report.GatewayStatus = gatewayHostedStatusNotServed
	if nonDecisionCount == 0 {
		report.GatewayReason = "no active Inference Operator"
		return
	}
	report.GatewayReason = fmt.Sprintf("Inference Operator does not serve %s", providerClass)
}

// hostedProviderKeyName names the environment variable holding a hosted
// provider class's API key; an Inference Operator serves the class when the
// key is set as it enrolls.
func hostedProviderKeyName(class string) string {
	key, ok := constants.HostedInferenceProviderAPIKeyEnv(class)
	if !ok {
		return "the provider's API key"
	}
	return string(key)
}

func formatGatewayHostedStatus(report *agentHostedProviderReport) string {
	switch report.GatewayStatus {
	case gatewayHostedStatusServed:
		return fmt.Sprintf("served (%s)", strings.Join(report.ServedByOperators, ", "))
	case gatewayHostedStatusNotServed:
		return fmt.Sprintf("not served (%s; set %s on an Inference Operator's host and restart it to re-enroll, or restart the Gateway for its embedded Operator)", report.GatewayReason, hostedProviderKeyName(report.ProviderClass))
	default:
		return fmt.Sprintf("unavailable (%s)", report.GatewayReason)
	}
}

func formatEvalRegistryModels(report *agentHostedProviderReport) string {
	switch report.EvalRegistryStatus {
	case evalRegistryStatusLoaded:
		tags := make([]string, 0, len(report.EvalRegistryModels))
		for _, m := range report.EvalRegistryModels {
			tags = append(tags, m.ServedModelTag)
		}
		return strings.Join(tags, ", ")
	case evalRegistryStatusEmpty:
		return "none"
	default:
		return fmt.Sprintf("unavailable (%s)", report.EvalRegistryReason)
	}
}

func printAgentHostedVerifyReport(cmd *cobra.Command, report *agentHostedProviderReport) {
	cmd.Printf("Hosted provider (%s): Gateway Inference Operator %s\n", report.ProviderClass, formatGatewayHostedStatus(report))
	cmd.Printf("Eval registry (%s): %s\n", report.ProviderClass, formatEvalRegistryModels(report))
}

func printAgentHostedShowBox(cmd *cobra.Command, report *agentHostedProviderReport) {
	cmd.Println()
	cmd.Println("┌─ Hosted Inference & Eval Registry ───────────────────────────────────────────")
	cmd.Printf("│ Provider class: %s\n", report.ProviderClass)
	cmd.Printf("│ Gateway Inference Operator: %s\n", formatGatewayHostedStatus(report))
	cmd.Printf("│ Eval registry models: %s\n", formatEvalRegistryModels(report))
	cmd.Println("└─────────────────────────────────────────────────────────────────────────────")
}

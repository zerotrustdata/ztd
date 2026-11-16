// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// Provider class taxonomy matching internal/constants/inference.go and hosted
// telemetry labels for the evaluation explorer.

import type { ModelRole, ModelSummary } from '../contract/types';
import { modelRecordKey, resolveModelSummary } from '../state/store';

/** Hosted inference provider classes (weights stay with the vendor). */
export const HOSTED_PROVIDER_CLASSES = ['gemini', 'anthropic', 'openai'] as const;
export type HostedProviderClass = (typeof HOSTED_PROVIDER_CLASSES)[number];

/** Local inference provider classes (weights served on an observed host). */
export const LOCAL_PROVIDER_CLASSES = ['ollama', 'llamacpp'] as const;
export type LocalProviderClass = (typeof LOCAL_PROVIDER_CLASSES)[number];

/** Explicit label rendered in GPU / provider-boundary telemetry panels for hosted cells. */
export const HOSTED_NO_TELEMETRY_LABEL = 'hosted: no provider-boundary telemetry';

export function isHostedProviderClass(providerClass?: string): boolean {
  if (!providerClass) return false;
  return (HOSTED_PROVIDER_CLASSES as readonly string[]).includes(providerClass);
}

export function isLocalProviderClass(providerClass?: string): boolean {
  if (!providerClass) return false;
  return (LOCAL_PROVIDER_CLASSES as readonly string[]).includes(providerClass);
}

export function formatProviderClassLabel(providerClass?: string): string {
  if (!providerClass) return '';
  return isHostedProviderClass(providerClass) ? `${providerClass} (hosted)` : providerClass;
}

/** Resolves a variant's backend_provider_class from normalized model summaries in the store. */
export function resolveVariantProviderClass(
  models: ReadonlyMap<string, ModelSummary>,
  datasetId: string,
  variantId?: string,
  role?: ModelRole,
): string | undefined {
  if (!variantId) return undefined;
  if (role) {
    const exact = models.get(modelRecordKey(datasetId, variantId, role));
    if (exact?.backend_provider_class) return exact.backend_provider_class;
  }
  const inDataset = resolveModelSummary(models as Map<string, ModelSummary>, datasetId, variantId);
  if (inDataset?.backend_provider_class) return inDataset.backend_provider_class;
  for (const candidate of models.values()) {
    if (candidate.variant_id === variantId && candidate.backend_provider_class) {
      return candidate.backend_provider_class;
    }
  }
  return undefined;
}

/** Normalizes missing provider-boundary telemetry reasons for hosted cells so they
 *  never surface as "source not captured". */
export function formatTelemetryUnavailableReason(reason: string, providerClass?: string): string {
  if (!isHostedProviderClass(providerClass)) return reason;
  const normalized = reason.trim().toLowerCase();
  if (normalized === 'source_not_captured' || normalized === 'source not captured') {
    return HOSTED_NO_TELEMETRY_LABEL;
  }
  return reason;
}

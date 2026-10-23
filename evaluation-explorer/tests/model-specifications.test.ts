// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { describe, expect, it } from 'vitest';
import { resolveModelProfile } from '../src/content/framework-catalog';
import { fixtureModelSummaries } from '../src/fixtures/fixtures';

describe('published model specifications', () => {
  const model = fixtureModelSummaries.find((entry) => entry.variant_id === 'gemma4-e4b')!;

  it('uses the representation frozen for the deployed variant rather than the catalog', () => {
    expect(resolveModelProfile({ ...model, quantization_weight_class: 'Q8_0' }).quantization).toBe('Q8_0');
    expect(resolveModelProfile({ ...model, quantization_weight_class: 'BF16' }).quantization).toBe('BF16');
  });

  it('leaves unreported specifications unavailable even for a known model', () => {
    const profile = resolveModelProfile({ ...model, served_model_tag: undefined, parameter_count: undefined, quantization_weight_class: undefined });
    expect(profile.served_model_tag).toBeUndefined();
    expect(profile.parameter_count).toBeUndefined();
    expect(profile.quantization).toBeUndefined();
    expect(profile.context_limit).toBeUndefined();
  });

  it('keeps installed artifact bytes separate from parameter count and runtime memory', () => {
    const profile = resolveModelProfile({ ...model, model_digest: 'a'.repeat(64), model_format: 'gguf', artifact_size_bytes: 5 * 1024 ** 3, context_limit: 4096 });
    expect(profile.parameter_count).toBe(8_000_000_000);
    expect(profile.artifact_size_bytes).toBe(5 * 1024 ** 3);
    expect(profile.artifact_label).toBe('5.00 GiB');
    expect(profile.context_limit).toBe(4096);
    expect(profile.model_format).toBe('gguf');
    expect(profile.model_digest).toBe('a'.repeat(64));
  });
});

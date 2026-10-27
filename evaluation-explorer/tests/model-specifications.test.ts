// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { describe, expect, it } from 'vitest';
import { resolveModelProfile } from '../src/content/framework-catalog';
import { fixtureModelSummaries } from '../src/fixtures/fixtures';

describe('published model specifications', () => {
  const model = fixtureModelSummaries.find((entry) => entry.variant_id === 'gemma4-e4b')!;

  it.each([
    { variant_id: 'hf-co-nvidia-nvidia-nemotron-3-5-bolt-e4b-4gb-q4-k-m', display_name: 'NVIDIA Nemotron 3.5 Bolt E4B', model_family: 'gemma4' },
    { variant_id: 'randomblock1-nemotron-nano-8b', display_name: 'Nemotron Nano', model_family: 'llama' },
    { variant_id: 'nemotron-3-nano-4b', display_name: 'Nemotron Nano', model_family: 'nemotron_h' },
    { variant_id: 'custom-model', display_name: 'Custom model', served_model_tag: 'hf.co/nvidia/NVIDIA-Nemotron-3.5-Bolt-E4B-4GB:Q4_K_M', model_family: 'gemma4' },
    { variant_id: 'custom-model', display_name: 'NVIDIA Nemotron Bolt', model_family: undefined },
  ])('identifies Nemotron independently of its base architecture: $variant_id / $display_name', (identity) => {
    const profile = resolveModelProfile({ ...model, served_model_tag: undefined, ...identity });
    expect(profile.family).toBe('Nemotron');
    expect(profile.family_key).toBe('nemotron');
    expect(profile.provider).toBe('NVIDIA');
  });

  it('keeps original Gemma identity and the DeepSeek derivative identity', () => {
    expect(resolveModelProfile({ ...model, model_family: 'gemma4' })).toMatchObject({ family: 'Gemma 4', provider: 'Google' });
    expect(resolveModelProfile({ ...model, variant_id: 'deepseek-r1-7b', display_name: 'DeepSeek R1', served_model_tag: 'deepseek-r1:7b', model_family: 'qwen2' })).toMatchObject({ family: 'DeepSeek R1', provider: 'DeepSeek' });
  });

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

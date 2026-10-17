// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// Enriched catalog metadata for the Models and Evaluations views:
// - Model hardware specs, clean served tags, family/provider taxonomy, and the 5 governed capability probes
// - Execution Topologies (Formations v1) with 3-role Lite -> Assistant -> Primary pipelines and VRAM budgets
// - Campaign aggregation and Evaluation Framework reference values

import type { EvaluationSummary, ModelRole, ModelSummary } from "../contract/types";

export type ModelCapabilityKey =
  | "completion"
  | "tool_calling"
  | "structured_output"
  | "thinking"
  | "context_limit";

export type CapabilityProbeStatus = "pass" | "fail" | "unsupported" | "unprobed";

export interface CapabilityObservation {
  key: ModelCapabilityKey;
  label: string;
  shortLabel: string;
  status: CapabilityProbeStatus;
  detail: string;
}

export interface ModelCapabilityMeta {
  key: ModelCapabilityKey;
  label: string;
  shortLabel: string;
  description: string;
}

export const MODEL_CAPABILITY_KEYS: readonly ModelCapabilityKey[] = [
  "completion",
  "tool_calling",
  "structured_output",
  "thinking",
  "context_limit",
] as const;

export const MODEL_CAPABILITY_META: Record<ModelCapabilityKey, ModelCapabilityMeta> = {
  completion: {
    key: "completion",
    label: "Completion",
    shortLabel: "Comp",
    description: "Deterministic text completion and instruction response probe",
  },
  tool_calling: {
    key: "tool_calling",
    label: "Tool Calling",
    shortLabel: "Tools",
    description: "Governed MCP / native tool invocation probe (probe_echo)",
  },
  structured_output: {
    key: "structured_output",
    label: "Structured JSON",
    shortLabel: "JSON",
    description: "Schema-constrained JSON payload generation and validation probe",
  },
  thinking: {
    key: "thinking",
    label: "Reasoning / Thinking",
    shortLabel: "Think",
    description: "Deliberative reasoning trace and multi-step answer verification probe",
  },
  context_limit: {
    key: "context_limit",
    label: "Context Window",
    shortLabel: "Ctx",
    description: "Provider-advertised and verified context window capacity",
  },
};

interface RawInventoryEntry {
  tag: string;
  family: string;
  params: number;
  quant: string;
  ctx: number;
  caps: string;
}

const RAW_MODEL_INVENTORY: Record<string, RawInventoryEntry> = {
  "impulse2000-smollm3-3b-q4-k-m": {
    "tag": "Impulse2000/smollm3:3b-q4_k_m",
    "family": "smollm3",
    "params": 3100000000,
    "quant": "Q4_K_M",
    "ctx": 65536,
    "caps": "PFPPP"
  },
  "impulse2000-smollm3-latest": {
    "tag": "Impulse2000/smollm3:latest",
    "family": "smollm3",
    "params": 3100000000,
    "quant": "Q4_K_M",
    "ctx": 65536,
    "caps": "PFPPP"
  },
  "randomblock1-nemotron-nano-8b": {
    "tag": "Randomblock1/nemotron-nano:8b",
    "family": "llama",
    "params": 8000000000,
    "quant": "Q4_K_M",
    "ctx": 131072,
    "caps": "PPPPP"
  },
  "deepseek-r1-7b": {
    "tag": "deepseek-r1:7b",
    "family": "qwen2",
    "params": 7600000000,
    "quant": "Q4_K_M",
    "ctx": 131072,
    "caps": "FFFPP"
  },
  "gemma2-2b-instruct-q4-k-m": {
    "tag": "gemma2:2b-instruct-q4_K_M",
    "family": "gemma2",
    "params": 2600000000,
    "quant": "Q4_K_M",
    "ctx": 8192,
    "caps": "PUPUP"
  },
  "gemma2-9b": {
    "tag": "gemma2:9b",
    "family": "gemma2",
    "params": 9200000000,
    "quant": "Q4_0",
    "ctx": 8192,
    "caps": "PUPUP"
  },
  "gemma2-9b-instruct-q4-k-m": {
    "tag": "gemma2:9b-instruct-q4_K_M",
    "family": "gemma2",
    "params": 9200000000,
    "quant": "Q4_K_M",
    "ctx": 8192,
    "caps": "PUPUP"
  },
  "gemma3-1b": {
    "tag": "gemma3:1b",
    "family": "gemma3",
    "params": 999890000,
    "quant": "Q4_K_M",
    "ctx": 32768,
    "caps": "PUFUP"
  },
  "gemma3-270m": {
    "tag": "gemma3:270m",
    "family": "gemma3",
    "params": 268100000,
    "quant": "Q8_0",
    "ctx": 32768,
    "caps": "PUPUP"
  },
  "gemma3-4b": {
    "tag": "gemma3:4b",
    "family": "gemma3",
    "params": 4300000000,
    "quant": "Q4_K_M",
    "ctx": 131072,
    "caps": "PUPUP"
  },
  "gemma4-12b": {
    "tag": "gemma4:12b",
    "family": "gemma4",
    "params": 11900000000,
    "quant": "Q4_K_M",
    "ctx": 262144,
    "caps": "PPPPP"
  },
  "gemma4-e2b": {
    "tag": "gemma4:e2b",
    "family": "gemma4",
    "params": 5100000000,
    "quant": "Q4_K_M",
    "ctx": 131072,
    "caps": "PPPPP"
  },
  "gemma4-e4b": {
    "tag": "gemma4:e4b",
    "family": "gemma4",
    "params": 8000000000,
    "quant": "Q4_K_M",
    "ctx": 131072,
    "caps": "PPPPP"
  },
  "granite3-3-2b": {
    "tag": "granite3.3:2b",
    "family": "granite",
    "params": 2500000000,
    "quant": "Q4_K_M",
    "ctx": 131072,
    "caps": "PPPUP"
  },
  "granite3-3-8b": {
    "tag": "granite3.3:8b",
    "family": "granite",
    "params": 8199999999,
    "quant": "Q4_K_M",
    "ctx": 131072,
    "caps": "PPPUP"
  },
  "granite4-2-3b": {
    "tag": "granite4.2:3b",
    "family": "granite",
    "params": 3660000000,
    "quant": "Q4_K_M",
    "ctx": 131072,
    "caps": "PPPPP"
  },
  "granite4-2-8b": {
    "tag": "granite4.2:8b",
    "family": "granite",
    "params": 8790000000,
    "quant": "Q4_K_M",
    "ctx": 131072,
    "caps": "PPPPP"
  },
  "hermes3-8b": {
    "tag": "hermes3:8b",
    "family": "llama",
    "params": 8000000000,
    "quant": "Q4_0",
    "ctx": 131072,
    "caps": "PPPUP"
  },
  "hf-co-nekuromento-toolace-8b-q8-0-gguf-latest": {
    "tag": "hf.co/Nekuromento/ToolACE-8B-Q8_0-GGUF:latest",
    "family": "llama",
    "params": 8029999999,
    "quant": "Q8_0",
    "ctx": 131072,
    "caps": "PUFUP"
  },
  "hf-co-salesforce-llama-xlam-2-8b-fc-r-gguf-latest": {
    "tag": "hf.co/Salesforce/Llama-xLAM-2-8b-fc-r-gguf:latest",
    "family": "llama",
    "params": 8029999999,
    "quant": "Q4_K_M",
    "ctx": 131072,
    "caps": "FPPUP"
  },
  "hf-co-salesforce-xlam-2-1b-fc-r-gguf-latest": {
    "tag": "hf.co/Salesforce/xLAM-2-1b-fc-r-gguf:latest",
    "family": "qwen2",
    "params": 1540000000,
    "quant": "Q4_K_M",
    "ctx": 32768,
    "caps": "FFPUP"
  },
  "hf-co-salesforce-xlam-2-3b-fc-r-gguf-latest": {
    "tag": "hf.co/Salesforce/xLAM-2-3b-fc-r-gguf:latest",
    "family": "qwen2",
    "params": 3090000000,
    "quant": "Q4_K_M",
    "ctx": 32768,
    "caps": "PFPUP"
  },
  "hf-co-bartowski-llama-3-2-1b-instruct-gguf-q4-k-m": {
    "tag": "hf.co/bartowski/Llama-3.2-1B-Instruct-GGUF:Q4_K_M",
    "family": "llama",
    "params": 1240000000,
    "quant": "Q4_K_M",
    "ctx": 131072,
    "caps": "FPPUP"
  },
  "hf-co-ibm-granite-granite-4-2-3b-gguf-q4-k-m": {
    "tag": "hf.co/ibm-granite/granite-4.2-3b-GGUF:Q4_K_M",
    "family": "granite",
    "params": 3660000000,
    "quant": "Q4_K_M",
    "ctx": 131072,
    "caps": "PPPPP"
  },
  "hf-co-ibm-granite-granite-4-2-8b-gguf-q4-k-m": {
    "tag": "hf.co/ibm-granite/granite-4.2-8b-GGUF:Q4_K_M",
    "family": "granite",
    "params": 8790000000,
    "quant": "Q4_K_M",
    "ctx": 131072,
    "caps": "PPPPP"
  },
  "hf-co-unsloth-glm-4-9b-0414-gguf-q4-k-m": {
    "tag": "hf.co/unsloth/GLM-4-9B-0414-GGUF:Q4_K_M",
    "family": "glm4",
    "params": 9400000000,
    "quant": "Q4_K_M",
    "ctx": 32768,
    "caps": "PFPUP"
  },
  "hf-co-unsloth-qwen3-5-0-8b-gguf-q4-k-m": {
    "tag": "hf.co/unsloth/Qwen3.5-0.8B-GGUF:Q4_K_M",
    "family": "qwen35",
    "params": 752000000,
    "quant": "Q4_K_M",
    "ctx": 262144,
    "caps": "PPPPP"
  },
  "hf-co-unsloth-qwen3-5-2b-gguf-q4-k-m": {
    "tag": "hf.co/unsloth/Qwen3.5-2B-GGUF:Q4_K_M",
    "family": "qwen35",
    "params": 1880000000,
    "quant": "Q4_K_M",
    "ctx": 262144,
    "caps": "PPFPP"
  },
  "hf-co-unsloth-qwen3-5-4b-gguf-q4-k-m": {
    "tag": "hf.co/unsloth/Qwen3.5-4B-GGUF:Q4_K_M",
    "family": "qwen35",
    "params": 4210000000,
    "quant": "Q4_K_M",
    "ctx": 262144,
    "caps": "PPPPP"
  },
  "hf-co-unsloth-qwen3-5-9b-gguf-q4-k-m": {
    "tag": "hf.co/unsloth/Qwen3.5-9B-GGUF:Q4_K_M",
    "family": "qwen35",
    "params": 8950000000,
    "quant": "Q4_K_M",
    "ctx": 262144,
    "caps": "PPPPP"
  },
  "hf-co-unsloth-gemma-4-12b-it-gguf-q4-k-m": {
    "tag": "hf.co/unsloth/gemma-4-12b-it-GGUF:Q4_K_M",
    "family": "gemma4",
    "params": 11900000000,
    "quant": "Q4_K_M",
    "ctx": 262144,
    "caps": "PPPUP"
  },
  "huggingface-co-unsloth-glm-4-7-flash-gguf-q4-k-m": {
    "tag": "huggingface.co/unsloth/GLM-4.7-Flash-GGUF:Q4_K_M",
    "family": "deepseek2",
    "params": 29900000000,
    "quant": "Q4_K_M",
    "ctx": 202752,
    "caps": "PPPPP"
  },
  "huggingface-co-unsloth-qwen3-6-35b-a3b-gguf-ud-q4-k-m": {
    "tag": "huggingface.co/unsloth/Qwen3.6-35B-A3B-GGUF:UD-Q4_K_M",
    "family": "qwen35moe",
    "params": 34700000000,
    "quant": "Q4_K_M",
    "ctx": 262144,
    "caps": "PPPPP"
  },
  "huggingface-co-unsloth-qwen3-8-27b-gguf-ud-q4-k-m": {
    "tag": "huggingface.co/unsloth/Qwen3.8-27B-GGUF:UD-Q4_K_M",
    "family": "qwen35",
    "params": 27300000000,
    "quant": "Q4_K_M",
    "ctx": 262144,
    "caps": "PPPPP"
  },
  "kitsonk-watt-tool-8b-latest": {
    "tag": "kitsonk/watt-tool-8B:latest",
    "family": "llama",
    "params": 8000000000,
    "quant": "Q4_K_M",
    "ctx": 131072,
    "caps": "PFPUP"
  },
  "llama3-1-8b": {
    "tag": "llama3.1:8b",
    "family": "llama",
    "params": 8000000000,
    "quant": "Q4_K_M",
    "ctx": 131072,
    "caps": "PPPUP"
  },
  "llama3-1-8b-instruct-q4-k-m": {
    "tag": "llama3.1:8b-instruct-q4_K_M",
    "family": "llama",
    "params": 8000000000,
    "quant": "Q4_K_M",
    "ctx": 131072,
    "caps": "PPPUP"
  },
  "llama3-2-1b": {
    "tag": "llama3.2:1b",
    "family": "llama",
    "params": 1200000000,
    "quant": "Q8_0",
    "ctx": 131072,
    "caps": "FPPUP"
  },
  "llama3-2-1b-instruct-q4-k-m": {
    "tag": "llama3.2:1b-instruct-q4_K_M",
    "family": "llama",
    "params": 1200000000,
    "quant": "Q4_K_M",
    "ctx": 131072,
    "caps": "PPPUP"
  },
  "llama3-2-3b": {
    "tag": "llama3.2:3b",
    "family": "llama",
    "params": 3200000000,
    "quant": "Q4_K_M",
    "ctx": 131072,
    "caps": "PPPUP"
  },
  "milkey-glm-4-9b-0414-q4-k-m": {
    "tag": "milkey/GLM-4-9B-0414:Q4_K_M",
    "family": "glm4",
    "params": 9400000000,
    "quant": "Q4_K_M",
    "ctx": 32768,
    "caps": "PFPUP"
  },
  "ministral-3-3b": {
    "tag": "ministral-3:3b",
    "family": "mistral3",
    "params": 3800000000,
    "quant": "Q4_K_M",
    "ctx": 262144,
    "caps": "PPPUP"
  },
  "ministral-3-8b": {
    "tag": "ministral-3:8b",
    "family": "mistral3",
    "params": 8900000000,
    "quant": "Q4_K_M",
    "ctx": 262144,
    "caps": "PPPUP"
  },
  "mistral-7b": {
    "tag": "mistral:7b",
    "family": "llama",
    "params": 7200000000,
    "quant": "Q4_K_M",
    "ctx": 32768,
    "caps": "PPPUP"
  },
  "nemotron-3-nano-4b": {
    "tag": "nemotron-3-nano:4b",
    "family": "nemotron_h",
    "params": 4000000000,
    "quant": "Q4_K_M",
    "ctx": 262144,
    "caps": "PPPPP"
  },
  "ornith-1-5-9b": {
    "tag": "ornith-1.5:9b",
    "family": "qwen35",
    "params": 9000000000,
    "quant": "Q4_K_M",
    "ctx": 262144,
    "caps": "PPPPP"
  },
  "phi3-5-3-8b-mini-instruct-q4-k-m": {
    "tag": "phi3.5:3.8b-mini-instruct-q4_K_M",
    "family": "phi3",
    "params": 3800000000,
    "quant": "Q4_K_M",
    "ctx": 131072,
    "caps": "PUFUP"
  },
  "phi4-mini-reasoning-3-8b": {
    "tag": "phi4-mini-reasoning:3.8b",
    "family": "phi3",
    "params": 3800000000,
    "quant": "Q4_K_M",
    "ctx": 131072,
    "caps": "PFPUP"
  },
  "phi4-mini-3-8b": {
    "tag": "phi4-mini:3.8b",
    "family": "phi3",
    "params": 3800000000,
    "quant": "Q4_K_M",
    "ctx": 131072,
    "caps": "PFPUP"
  },
  "phi4-mini-latest": {
    "tag": "phi4-mini:latest",
    "family": "phi3",
    "params": 3800000000,
    "quant": "Q4_K_M",
    "ctx": 131072,
    "caps": "PFPUP"
  },
  "qwen2-5-coder-7b": {
    "tag": "qwen2.5-coder:7b",
    "family": "qwen2",
    "params": 7600000000,
    "quant": "Q4_K_M",
    "ctx": 32768,
    "caps": "PFPUP"
  },
  "qwen2-5-coder-7b-instruct-q4-k-m": {
    "tag": "qwen2.5-coder:7b-instruct-q4_K_M",
    "family": "qwen2",
    "params": 7600000000,
    "quant": "Q4_K_M",
    "ctx": 32768,
    "caps": "PFPUP"
  },
  "qwen2-5-0-5b": {
    "tag": "qwen2.5:0.5b",
    "family": "qwen2",
    "params": 494030000,
    "quant": "Q4_K_M",
    "ctx": 32768,
    "caps": "PPPUP"
  },
  "qwen2-5-0-5b-instruct-q4-k-m": {
    "tag": "qwen2.5:0.5b-instruct-q4_K_M",
    "family": "qwen2",
    "params": 494030000,
    "quant": "Q4_K_M",
    "ctx": 32768,
    "caps": "PPPUP"
  },
  "qwen2-5-1-5b-instruct-q4-k-m": {
    "tag": "qwen2.5:1.5b-instruct-q4_K_M",
    "family": "qwen2",
    "params": 1500000000,
    "quant": "Q4_K_M",
    "ctx": 32768,
    "caps": "PPPUP"
  },
  "qwen2-5-14b-instruct-q4-k-m": {
    "tag": "qwen2.5:14b-instruct-q4_K_M",
    "family": "qwen2",
    "params": 14800000000,
    "quant": "Q4_K_M",
    "ctx": 32768,
    "caps": "PPPUP"
  },
  "qwen2-5-3b": {
    "tag": "qwen2.5:3b",
    "family": "qwen2",
    "params": 3100000000,
    "quant": "Q4_K_M",
    "ctx": 32768,
    "caps": "PPPUP"
  },
  "qwen2-5-7b": {
    "tag": "qwen2.5:7b",
    "family": "qwen2",
    "params": 7600000000,
    "quant": "Q4_K_M",
    "ctx": 32768,
    "caps": "PPPUP"
  },
  "qwen3-5-0-8b": {
    "tag": "qwen3.5:0.8b",
    "family": "qwen35",
    "params": 752000000,
    "quant": "Q4_K_M",
    "ctx": 262144,
    "caps": "PPPPP"
  },
  "qwen3-5-2b": {
    "tag": "qwen3.5:2b",
    "family": "qwen35",
    "params": 1880000000,
    "quant": "Q4_K_M",
    "ctx": 262144,
    "caps": "PPPPP"
  },
  "qwen3-5-4b": {
    "tag": "qwen3.5:4b",
    "family": "qwen35",
    "params": 4700000000,
    "quant": "Q4_K_M",
    "ctx": 262144,
    "caps": "PPPPP"
  },
  "qwen3-5-9b": {
    "tag": "qwen3.5:9b",
    "family": "qwen35",
    "params": 9700000000,
    "quant": "Q4_K_M",
    "ctx": 262144,
    "caps": "PPPPP"
  },
  "qwen3-6-35b-a3b": {
    "tag": "qwen3.6:35b-a3b",
    "family": "qwen35moe",
    "params": 34700000000,
    "quant": "Q4_K_M",
    "ctx": 262144,
    "caps": "PPPPP"
  },
  "qwen3-8-27b": {
    "tag": "qwen3.8:27b",
    "family": "qwen35",
    "params": 27300000000,
    "quant": "Q4_K_M",
    "ctx": 262144,
    "caps": "PPPPP"
  },
  "qwen3-0-6b": {
    "tag": "qwen3:0.6b",
    "family": "qwen3",
    "params": 751630000,
    "quant": "Q4_K_M",
    "ctx": 40960,
    "caps": "PPPPP"
  },
  "qwen3-1-7b": {
    "tag": "qwen3:1.7b",
    "family": "qwen3",
    "params": 2000000000,
    "quant": "Q4_K_M",
    "ctx": 40960,
    "caps": "PPPPP"
  },
  "qwen3-4b": {
    "tag": "qwen3:4b",
    "family": "qwen3",
    "params": 4000000000,
    "quant": "Q4_K_M",
    "ctx": 262144,
    "caps": "PPPPP"
  },
  "qwen3-8b": {
    "tag": "qwen3:8b",
    "family": "qwen3",
    "params": 8199999999,
    "quant": "Q4_K_M",
    "ctx": 40960,
    "caps": "PPPPP"
  },
  "sam860-lfm2-2-6b": {
    "tag": "sam860/LFM2:2.6b",
    "family": "lfm2",
    "params": 2570000000,
    "quant": "Q5_K_M",
    "ctx": 128000,
    "caps": "PPPUP"
  },
  "sam860-lfm2-350m": {
    "tag": "sam860/LFM2:350m",
    "family": "lfm2",
    "params": 354000000,
    "quant": "Q8_0",
    "ctx": 128000,
    "caps": "PUPUP"
  },
  "sam860-lfm2-700m": {
    "tag": "sam860/LFM2:700m",
    "family": "lfm2",
    "params": 742000000,
    "quant": "Q8_0",
    "ctx": 128000,
    "caps": "PUPUP"
  },
  "smollm2-1-7b": {
    "tag": "smollm2:1.7b",
    "family": "llama",
    "params": 1700000000,
    "quant": "Q8_0",
    "ctx": 8192,
    "caps": "FPPUP"
  },
  "smollm2-135m": {
    "tag": "smollm2:135m",
    "family": "llama",
    "params": 134520000,
    "quant": "F16",
    "ctx": 8192,
    "caps": "PUPUP"
  },
  "smollm2-360m": {
    "tag": "smollm2:360m",
    "family": "llama",
    "params": 361820000,
    "quant": "F16",
    "ctx": 8192,
    "caps": "PUPUP"
  },
  "tinyllama-1-1b": {
    "tag": "tinyllama:1.1b",
    "family": "llama",
    "params": 1000000000,
    "quant": "Q4_0",
    "ctx": 2048,
    "caps": "FUFUP"
  },
  "gemma4-12b-it-qat": {
    "tag": "gemma4:12b-it-qat",
    "family": "gemma4",
    "params": 11900000000,
    "quant": "Q4_0",
    "ctx": 262144,
    "caps": "PPPPP"
  },
  "gemma4-e4b-it-qat": {
    "tag": "gemma4:e4b-it-qat",
    "family": "gemma4",
    "params": 7500000000,
    "quant": "Q4_0",
    "ctx": 131072,
    "caps": "PPPPP"
  },
  "glm-4-7-flash-latest": {
    "tag": "glm-4.7-flash:latest",
    "family": "glm4moelite",
    "params": 29900000000,
    "quant": "Q4_K_M",
    "ctx": 202752,
    "caps": "PPPPP"
  },
  "gpt-oss-20b": {
    "tag": "gpt-oss:20b",
    "family": "gptoss",
    "params": 20900000000,
    "quant": "MXFP4",
    "ctx": 131072,
    "caps": "PPPPP"
  },
  "hf-co-hauhaucs-qwen3-5-4b-uncensored-hauhaucs-aggressive-q4-k-m": {
    "tag": "hf.co/HauhauCS/Qwen3.5-4B-Uncensored-HauhauCS-Aggressive:Q4_K_M",
    "family": "qwen35",
    "params": 4210000000,
    "quant": "Q4_K_M",
    "ctx": 262144,
    "caps": "PPPPP"
  },
  "hf-co-jackrong-qwen3-5-4b-neo-gguf-q4-k-m": {
    "tag": "hf.co/Jackrong/Qwen3.5-4B-Neo-GGUF:Q4_K_M",
    "family": "qwen35",
    "params": 4210000000,
    "quant": "Q4_K_M",
    "ctx": 262144,
    "caps": "PPPPP"
  },
  "hf-co-jackrong-qwopus3-5-4b-coder-gguf-q4-k-m": {
    "tag": "hf.co/Jackrong/Qwopus3.5-4B-Coder-GGUF:Q4_K_M",
    "family": "qwen35",
    "params": 4330000000,
    "quant": "Q4_K_M",
    "ctx": 262144,
    "caps": "PPPPP"
  },
  "huggingface-co-unsloth-gpt-oss-20b-gguf-q4-0": {
    "tag": "huggingface.co/unsloth/gpt-oss-20b-GGUF:Q4_0",
    "family": "gpt-oss",
    "params": 20900000000,
    "quant": "Q4_0",
    "ctx": 131072,
    "caps": "PFPPP"
  },
  "nimble-9b": {
    "tag": "nimble:9b",
    "family": "qwen35",
    "params": 9000000000,
    "quant": "Q8_0",
    "ctx": 8194,
    "caps": "PPFPP"
  },
  "north-mini-code-1-0-latest": {
    "tag": "north-mini-code-1.0:latest",
    "family": "cohere2moe",
    "params": 30500000000,
    "quant": "Q4_K_M",
    "ctx": 500000,
    "caps": "PPPPP"
  },
  "ornith-9b": {
    "tag": "ornith:9b",
    "family": "qwen35",
    "params": 9000000000,
    "quant": "Q4_K_M",
    "ctx": 262144,
    "caps": "PPPPP"
  },
  "qwen2-5-0-5b-instruct": {
    "tag": "qwen2.5:0.5b-instruct",
    "family": "qwen2",
    "params": 494030000,
    "quant": "Q4_K_M",
    "ctx": 32768,
    "caps": "PPPUP"
  },
  "qwen3-6-35b-a3b-q4-k-m": {
    "tag": "qwen3.6:35b-a3b-q4_K_M",
    "family": "qwen35moe",
    "params": 36000000000,
    "quant": "Q4_K_M",
    "ctx": 262144,
    "caps": "PPPPP"
  },
  "smollm3-3b": {
    "tag": "smollm3:3b",
    "family": "smollm3",
    "params": 3100000000,
    "quant": "Q4_K_M",
    "ctx": 65536,
    "caps": "PFPPP"
  },
  "toolace-8b": {
    "tag": "toolace:8b",
    "family": "llama",
    "params": 8029999999,
    "quant": "Q8_0",
    "ctx": 131072,
    "caps": "PUFUP"
  },
  "watttool-8b": {
    "tag": "watttool:8b",
    "family": "llama",
    "params": 8000000000,
    "quant": "Q4_K_M",
    "ctx": 131072,
    "caps": "FFPUP"
  },
  "xlam2-1b-fc-r": {
    "tag": "xlam2:1b-fc-r",
    "family": "qwen2",
    "params": 1540000000,
    "quant": "Q4_K_M",
    "ctx": 32768,
    "caps": "FFPUP"
  },
  "xlam2-3b-fc-r": {
    "tag": "xlam2:3b-fc-r",
    "family": "qwen2",
    "params": 3090000000,
    "quant": "Q4_K_M",
    "ctx": 32768,
    "caps": "PFPUP"
  },
  "xlam2-8b-fc-r": {
    "tag": "xlam2:8b-fc-r",
    "family": "llama",
    "params": 8029999999,
    "quant": "Q4_K_M",
    "ctx": 131072,
    "caps": "FPPUP"
  }
};

const VARIANT_ALIASES: Record<string, string> = {
  "qwen35-9b": "qwen3-5-9b",
  "qwen35-4b": "qwen3-5-4b",
  "qwen35-2b": "qwen3-5-2b",
  "qwen35-08b": "qwen3-5-0-8b",
  "qwen25-7b": "qwen2-5-7b",
  "qwen25-15b": "qwen2-5-1-5b",
  "qwen25-05b": "qwen2-5-0-5b",
  "llama31-8b": "llama3-1-8b",
  "llama32-3b": "llama3-2-3b",
  "llama32-1b": "llama3-2-1b",
  "llama32-1b-gemma": "llama3-2-1b",
  "llama32-1b-speed": "llama3-2-1b",
  "llama3-1b": "llama3-2-1b",
  "ministral-3b": "ministral-3-3b",
  "ministral-3b-qwen": "ministral-3-3b",
  "phi4-mini-38b": "phi4-mini-3-8b",
  "gemma3-1b-llama": "gemma3-1b",
  "gemma4-e4b-alt": "gemma4-e4b",
  "gemma4-e2b-alt": "gemma4-e2b"
};

const FAMILY_LABELS: Record<string, { family: string; provider: string }> = {
  gemma2: { family: "Gemma 2", provider: "Google" },
  gemma3: { family: "Gemma 3", provider: "Google" },
  gemma4: { family: "Gemma 4", provider: "Google" },
  qwen2: { family: "Qwen 2.5", provider: "Alibaba" },
  qwen3: { family: "Qwen 3", provider: "Alibaba" },
  qwen35: { family: "Qwen 3.5", provider: "Alibaba" },
  llama: { family: "Llama 3", provider: "Meta" },
  mistral3: { family: "Mistral 3", provider: "Mistral" },
  phi3: { family: "Phi 3.5 / 4", provider: "Microsoft" },
  phi4: { family: "Phi 4", provider: "Microsoft" },
  granite: { family: "Granite", provider: "IBM" },
  granitehybrid: { family: "Granite 4.2", provider: "IBM" },
  glm4: { family: "GLM 4", provider: "Zhipu GLM" },
  smollm3: { family: "SmolLM 3", provider: "HuggingFace" },
  lfm2: { family: "LFM2", provider: "Liquid AI" },
};

export function formatParameterCount(params?: number): string | undefined {
  if (!params || params <= 0) return undefined;
  if (params >= 1_000_000_000) {
    const b = params / 1_000_000_000;
    return `${b >= 10 ? b.toFixed(0) : b.toFixed(1).replace(/\.0$/, "")}B`;
  }
  const m = Math.round(params / 1_000_000);
  return `${m}M`;
}

export function formatContextWindow(ctx?: number): string | undefined {
  if (!ctx || ctx <= 0) return undefined;
  if (ctx >= 1024) {
    return `${Math.round(ctx / 1024)}K`;
  }
  return String(ctx);
}

function decodeCapabilityStatus(code: string): CapabilityProbeStatus {
  switch (code) {
    case "P":
      return "pass";
    case "F":
      return "fail";
    case "U":
      return "unsupported";
    default:
      return "unprobed";
  }
}

function capabilityDetailFor(
  key: ModelCapabilityKey,
  status: CapabilityProbeStatus,
  ctx?: number,
): string {
  if (status === "unprobed") return "Capability probe not yet recorded for this variant";
  switch (key) {
    case "completion":
      return status === "pass"
        ? "completion probe returned expected text"
        : "completion probe returned unexpected output";
    case "tool_calling":
      if (status === "pass") return "tool-call probe returned probe_echo";
      if (status === "unsupported") return "provider rejected tool-call probe";
      return "tool-call probe did not return probe_echo";
    case "structured_output":
      return status === "pass"
        ? "structured-output probe returned valid JSON"
        : "structured-output probe returned invalid schema payload";
    case "thinking":
      if (status === "pass") return "thinking probe returned visible thinking or expected answer text";
      if (status === "unsupported") return "provider rejected thinking probe";
      return "thinking probe did not return expected reasoning trace";
    case "context_limit":
      return status === "pass" && ctx
        ? `provider advertised context_limit=${ctx.toLocaleString("en-US")}`
        : "context window capacity verified via provider metadata";
  }
}

function isMangledServedTag(tag: string | undefined, variantId: string): boolean {
  if (!tag) return true;
  // Detect projector fallback tags like "gemma22binstructq4k:m" where all dashes were stripped
  const parts = variantId.split("-");
  if (parts.length >= 3) {
    const mangled = parts.slice(0, -1).join("") + ":" + parts[parts.length - 1];
    if (tag.toLowerCase() === mangled.toLowerCase()) return true;
  }
  return false;
}

export interface ResolvedModelProfile {
  variant_id: string;
  served_model_tag?: string;
  family: string;
  family_key: string;
  provider: string;
  parameter_count?: number;
  parameter_label?: string;
  quantization?: string;
  context_limit?: number;
  context_label?: string;
  capabilities: Record<ModelCapabilityKey, CapabilityObservation>;
  capability_list: CapabilityObservation[];
  passed_capability_count: number;
  probed_capability_count: number;
}

export function resolveModelProfile(
  model: Pick<
    ModelSummary,
    "variant_id" | "display_name" | "served_model_tag" | "backend_provider_class" | "quantization_weight_class"
  >,
): ResolvedModelProfile {
  const canonicalId = VARIANT_ALIASES[model.variant_id] ?? model.variant_id;
  const raw = RAW_MODEL_INVENTORY[canonicalId] ?? RAW_MODEL_INVENTORY[model.variant_id];

  const familyKey = raw?.family ?? inferFamilyKey(model.variant_id, model.display_name);
  const familyInfo = FAMILY_LABELS[familyKey] ?? {
    family: capitalizeWords(familyKey),
    provider: model.backend_provider_class ?? "Ollama",
  };

  // Refine DeepSeek R1 family label when served tag is deepseek-r1
  const isDeepSeek = model.variant_id.includes("deepseek") || raw?.tag?.includes("deepseek");
  const familyLabel = isDeepSeek ? "DeepSeek R1" : familyInfo.family;
  const providerLabel = isDeepSeek ? "DeepSeek" : familyInfo.provider;

  const servedTag =
    raw && isMangledServedTag(model.served_model_tag, model.variant_id)
      ? raw.tag
      : model.served_model_tag ?? raw?.tag;

  const parameterCount = raw?.params || inferParameterCount(model.variant_id, model.display_name, model.served_model_tag);
  const parameterLabel = formatParameterCount(parameterCount);
  const quantization =
    raw?.quant ||
    (model.quantization_weight_class ? model.quantization_weight_class.toUpperCase() : inferQuantization(model.variant_id, servedTag));
  const contextLimit = raw?.ctx || undefined;
  const contextLabel = formatContextWindow(contextLimit);

  const capCodes = raw?.caps ?? "NNNNN";
  const capabilities = {} as Record<ModelCapabilityKey, CapabilityObservation>;
  const capabilityList: CapabilityObservation[] = [];
  let passedCount = 0;
  let probedCount = 0;

  MODEL_CAPABILITY_KEYS.forEach((key, idx) => {
    const meta = MODEL_CAPABILITY_META[key];
    const status = decodeCapabilityStatus(capCodes[idx] ?? "N");
    if (status !== "unprobed") probedCount += 1;
    if (status === "pass") passedCount += 1;
    const obs: CapabilityObservation = {
      key,
      label: meta.label,
      shortLabel: meta.shortLabel,
      status,
      detail: capabilityDetailFor(key, status, contextLimit),
    };
    capabilities[key] = obs;
    capabilityList.push(obs);
  });

  return {
    variant_id: model.variant_id,
    served_model_tag: servedTag,
    family: familyLabel,
    family_key: isDeepSeek ? "deepseek" : familyKey,
    provider: providerLabel,
    parameter_count: parameterCount,
    parameter_label: parameterLabel,
    quantization,
    context_limit: contextLimit,
    context_label: contextLabel,
    capabilities,
    capability_list: capabilityList,
    passed_capability_count: passedCount,
    probed_capability_count: probedCount,
  };
}

function inferFamilyKey(variantId: string, displayName: string): string {
  const lower = `${variantId} ${displayName}`.toLowerCase();
  if (lower.includes("gemma4") || lower.includes("gemma 4")) return "gemma4";
  if (lower.includes("gemma3") || lower.includes("gemma 3")) return "gemma3";
  if (lower.includes("gemma2") || lower.includes("gemma 2")) return "gemma2";
  if (lower.includes("qwen3.5") || lower.includes("qwen3-5") || lower.includes("qwen35") || lower.includes("qwen 3.5")) return "qwen35";
  if (lower.includes("qwen3") || lower.includes("qwen 3")) return "qwen3";
  if (lower.includes("qwen")) return "qwen2";
  if (lower.includes("llama")) return "llama";
  if (lower.includes("ministral") || lower.includes("mistral")) return "mistral3";
  if (lower.includes("phi4") || lower.includes("phi 4")) return "phi4";
  if (lower.includes("phi")) return "phi3";
  if (lower.includes("granite")) return "granite";
  if (lower.includes("glm")) return "glm4";
  if (lower.includes("smollm")) return "smollm3";
  if (lower.includes("lfm")) return "lfm2";
  return "general";
}

function inferParameterCount(variantId: string, displayName: string, servedTag?: string): number | undefined {
  const source = `${displayName} ${servedTag ?? ""} ${variantId}`;
  const mMatch = source.match(/\b(\d+(?:\.\d+)?)\s*[mM]\b/);
  if (mMatch) return Math.round(Number(mMatch[1]) * 1_000_000);
  const bMatch = source.match(/\b(?:[eE])?(\d+(?:\.\d+)?)\s*[bB]\b/);
  if (bMatch) return Math.round(Number(bMatch[1]) * 1_000_000_000);
  return undefined;
}

function inferQuantization(variantId: string, servedTag?: string): string | undefined {
  const source = `${servedTag ?? ""} ${variantId}`.toUpperCase();
  if (source.includes("Q4_K_M") || source.includes("Q4-K-M")) return "Q4_K_M";
  if (source.includes("Q8_0") || source.includes("Q8-0")) return "Q8_0";
  if (source.includes("Q4_0") || source.includes("Q4-0")) return "Q4_0";
  if (source.includes("FP16") || source.includes("F16")) return "FP16";
  return undefined;
}

function capitalizeWords(input: string): string {
  return input
    .split(/[-_\s]+/)
    .filter(Boolean)
    .map((w) => w.charAt(0).toUpperCase() + w.slice(1))
    .join(" ");
}

export interface FormationRoleBinding {
  variant_id: string;
  display_name: string;
  provider: string;
  family: string;
  provider_class: string;
  served_model_tag: string;
  trust: string;
  quantization: string;
  parameter_count: number;
  estimated_model_vram_mib: number;
  estimated_kv_cache_mib: number;
}

export interface FormationTopologySpec {
  id: string;
  display_name: string;
  description: string;
  kind: "curated" | "heterogeneous";
  max_vram_mib: number;
  primary: FormationRoleBinding;
  assistant: FormationRoleBinding;
  lite: FormationRoleBinding;
  total_vram_mib: number;
  vram_utilization_pct: number;
  provider_diversity_count: number;
  family_diversity_count: number;
}

const RAW_FORMATIONS: Array<Omit<FormationTopologySpec, "total_vram_mib" | "vram_utilization_pct" | "provider_diversity_count" | "family_diversity_count">> = [
  {
    "id": "qwen-powerhouse",
    "display_name": "Qwen Powerhouse",
    "description": "Qwen3.5 reasoning primary with a Ministral assistant and Gemma lite triage.",
    "kind": "curated",
    "max_vram_mib": 12288,
    "primary": {
      "variant_id": "qwen35-9b",
      "display_name": "Qwen 3.5 9B",
      "provider": "Alibaba",
      "family": "Qwen 3.5",
      "provider_class": "ollama",
      "served_model_tag": "qwen3.5:9b",
      "trust": "sovereign",
      "quantization": "Q4_K_M",
      "parameter_count": 9000000000,
      "estimated_model_vram_mib": 5632,
      "estimated_kv_cache_mib": 512
    },
    "assistant": {
      "variant_id": "ministral-3b-qwen",
      "display_name": "Ministral 3 3B",
      "provider": "Mistral",
      "family": "Mistral 3",
      "provider_class": "ollama",
      "served_model_tag": "ministral-3:3b",
      "trust": "sovereign",
      "quantization": "Q4_K_M",
      "parameter_count": 3800000000,
      "estimated_model_vram_mib": 2560,
      "estimated_kv_cache_mib": 512
    },
    "lite": {
      "variant_id": "gemma3-1b",
      "display_name": "Gemma 3 1B",
      "provider": "Google",
      "family": "Gemma 3",
      "provider_class": "ollama",
      "served_model_tag": "gemma3:1b",
      "trust": "sovereign",
      "quantization": "Q4_K_M",
      "parameter_count": 999890000,
      "estimated_model_vram_mib": 768,
      "estimated_kv_cache_mib": 256
    }
  },
  {
    "id": "deepseek-reasoning-tower",
    "display_name": "DeepSeek Reasoning Tower",
    "description": "DeepSeek R1 reasoning primary with efficient companions for fast triage and structured work.",
    "kind": "curated",
    "max_vram_mib": 12288,
    "primary": {
      "variant_id": "deepseek-r1-7b",
      "display_name": "DeepSeek R1 7B",
      "provider": "DeepSeek",
      "family": "DeepSeek",
      "provider_class": "ollama",
      "served_model_tag": "deepseek-r1:7b",
      "trust": "sovereign",
      "quantization": "Q4_K_M",
      "parameter_count": 7600000000,
      "estimated_model_vram_mib": 4736,
      "estimated_kv_cache_mib": 512
    },
    "assistant": {
      "variant_id": "ministral-3b",
      "display_name": "Ministral 3 3B",
      "provider": "Mistral",
      "family": "Mistral 3",
      "provider_class": "ollama",
      "served_model_tag": "ministral-3:3b",
      "trust": "sovereign",
      "quantization": "Q4_K_M",
      "parameter_count": 3800000000,
      "estimated_model_vram_mib": 2560,
      "estimated_kv_cache_mib": 512
    },
    "lite": {
      "variant_id": "llama32-1b",
      "display_name": "Llama 3.2 1B",
      "provider": "Meta",
      "family": "Llama 3.2",
      "provider_class": "ollama",
      "served_model_tag": "llama3.2:1b",
      "trust": "sovereign",
      "quantization": "Q8_0",
      "parameter_count": 1200000000,
      "estimated_model_vram_mib": 1024,
      "estimated_kv_cache_mib": 256
    }
  },
  {
    "id": "gemma-cascade",
    "display_name": "Gemma Cascade",
    "description": "Gemma 4 primary with a Qwen assistant and Llama lite triage.",
    "kind": "curated",
    "max_vram_mib": 12288,
    "primary": {
      "variant_id": "gemma4-e4b",
      "display_name": "Gemma 4 E4B",
      "provider": "Google",
      "family": "Gemma 4",
      "provider_class": "ollama",
      "served_model_tag": "gemma4:e4b",
      "trust": "sovereign",
      "quantization": "Q4_K_M",
      "parameter_count": 8000000000,
      "estimated_model_vram_mib": 5120,
      "estimated_kv_cache_mib": 512
    },
    "assistant": {
      "variant_id": "qwen25-7b",
      "display_name": "Qwen 2.5 7B",
      "provider": "Alibaba",
      "family": "Qwen 2.5",
      "provider_class": "ollama",
      "served_model_tag": "qwen2.5:7b",
      "trust": "sovereign",
      "quantization": "Q4_K_M",
      "parameter_count": 7600000000,
      "estimated_model_vram_mib": 4736,
      "estimated_kv_cache_mib": 512
    },
    "lite": {
      "variant_id": "llama32-1b-gemma",
      "display_name": "Llama 3.2 1B",
      "provider": "Meta",
      "family": "Llama 3.2",
      "provider_class": "ollama",
      "served_model_tag": "llama3.2:1b",
      "trust": "sovereign",
      "quantization": "Q8_0",
      "parameter_count": 1200000000,
      "estimated_model_vram_mib": 1024,
      "estimated_kv_cache_mib": 256
    }
  },
  {
    "id": "llama-meta-stack",
    "display_name": "Llama Meta Stack",
    "description": "Llama 3.1 primary with a Phi 4 assistant and Gemma lite triage.",
    "kind": "curated",
    "max_vram_mib": 12288,
    "primary": {
      "variant_id": "llama31-8b",
      "display_name": "Llama 3.1 8B",
      "provider": "Meta",
      "family": "Llama 3.1",
      "provider_class": "ollama",
      "served_model_tag": "llama3.1:8b",
      "trust": "sovereign",
      "quantization": "Q4_K_M",
      "parameter_count": 8000000000,
      "estimated_model_vram_mib": 5120,
      "estimated_kv_cache_mib": 512
    },
    "assistant": {
      "variant_id": "phi4-mini-38b",
      "display_name": "Phi4 Mini 3.8B",
      "provider": "Microsoft",
      "family": "Phi 4",
      "provider_class": "ollama",
      "served_model_tag": "phi4-mini:3.8b",
      "trust": "sovereign",
      "quantization": "Q4_K_M",
      "parameter_count": 3800000000,
      "estimated_model_vram_mib": 2560,
      "estimated_kv_cache_mib": 512
    },
    "lite": {
      "variant_id": "gemma3-1b-llama",
      "display_name": "Gemma 3 1B",
      "provider": "Google",
      "family": "Gemma 3",
      "provider_class": "ollama",
      "served_model_tag": "gemma3:1b",
      "trust": "sovereign",
      "quantization": "Q4_K_M",
      "parameter_count": 999890000,
      "estimated_model_vram_mib": 768,
      "estimated_kv_cache_mib": 256
    }
  },
  {
    "id": "ultra-efficient-speedster",
    "display_name": "Ultra-Efficient Speedster",
    "description": "Small, fast models across all roles for latency-sensitive tasks.",
    "kind": "curated",
    "max_vram_mib": 12288,
    "primary": {
      "variant_id": "qwen35-4b",
      "display_name": "Qwen 3.5 4B",
      "provider": "Alibaba",
      "family": "Qwen 3.5",
      "provider_class": "ollama",
      "served_model_tag": "qwen3.5:4b",
      "trust": "sovereign",
      "quantization": "Q4_K_M",
      "parameter_count": 4000000000,
      "estimated_model_vram_mib": 2560,
      "estimated_kv_cache_mib": 256
    },
    "assistant": {
      "variant_id": "gemma4-e2b",
      "display_name": "Gemma 4 E2B",
      "provider": "Google",
      "family": "Gemma 4",
      "provider_class": "ollama",
      "served_model_tag": "gemma4:e2b",
      "trust": "sovereign",
      "quantization": "Q4_K_M",
      "parameter_count": 5100000000,
      "estimated_model_vram_mib": 3200,
      "estimated_kv_cache_mib": 256
    },
    "lite": {
      "variant_id": "llama32-1b-speed",
      "display_name": "Llama 3.2 1B",
      "provider": "Meta",
      "family": "Llama 3.2",
      "provider_class": "ollama",
      "served_model_tag": "llama3.2:1b",
      "trust": "sovereign",
      "quantization": "Q8_0",
      "parameter_count": 1200000000,
      "estimated_model_vram_mib": 1024,
      "estimated_kv_cache_mib": 256
    }
  },
  {
    "id": "heterogeneous-0000",
    "display_name": "Heterogeneous 0000",
    "description": "Random 3-model formation: Mistral 3 + Qwen 2 + Gemma 3",
    "kind": "heterogeneous",
    "max_vram_mib": 12288,
    "primary": {
      "variant_id": "ministral-3-8b",
      "display_name": "Mistral 3 8.9b",
      "provider": "Mistral",
      "family": "Mistral 3",
      "provider_class": "ollama",
      "served_model_tag": "ministral-3:8b",
      "trust": "sovereign",
      "quantization": "Q4_K_M",
      "parameter_count": 8900000000,
      "estimated_model_vram_mib": 4481,
      "estimated_kv_cache_mib": 512
    },
    "assistant": {
      "variant_id": "qwen2-5-1-5b-instruct-q4-k-m",
      "display_name": "Qwen 2 1.5b",
      "provider": "Alibaba",
      "family": "Qwen 2",
      "provider_class": "ollama",
      "served_model_tag": "qwen2.5:1.5b-instruct-q4_K_M",
      "trust": "sovereign",
      "quantization": "Q4_K_M",
      "parameter_count": 1500000000,
      "estimated_model_vram_mib": 755,
      "estimated_kv_cache_mib": 512
    },
    "lite": {
      "variant_id": "gemma3-270m",
      "display_name": "Gemma 3 268m",
      "provider": "Google",
      "family": "Gemma 3",
      "provider_class": "ollama",
      "served_model_tag": "gemma3:270m",
      "trust": "sovereign",
      "quantization": "Q8_0",
      "parameter_count": 268000000,
      "estimated_model_vram_mib": 278,
      "estimated_kv_cache_mib": 256
    }
  },
  {
    "id": "heterogeneous-0001",
    "display_name": "Heterogeneous 0001",
    "description": "Random 3-model formation: Qwen 2 + Mistral 3 + Llama",
    "kind": "heterogeneous",
    "max_vram_mib": 12288,
    "primary": {
      "variant_id": "deepseek-r1-7b",
      "display_name": "Qwen 2 7.6b",
      "provider": "Alibaba",
      "family": "Qwen 2",
      "provider_class": "ollama",
      "served_model_tag": "deepseek-r1:7b",
      "trust": "sovereign",
      "quantization": "Q4_K_M",
      "parameter_count": 7600000000,
      "estimated_model_vram_mib": 3826,
      "estimated_kv_cache_mib": 512
    },
    "assistant": {
      "variant_id": "ministral-3-3b",
      "display_name": "Mistral 3 3.8b",
      "provider": "Mistral",
      "family": "Mistral 3",
      "provider_class": "ollama",
      "served_model_tag": "ministral-3:3b",
      "trust": "sovereign",
      "quantization": "Q4_K_M",
      "parameter_count": 3800000000,
      "estimated_model_vram_mib": 1913,
      "estimated_kv_cache_mib": 512
    },
    "lite": {
      "variant_id": "llama3-2-1b-instruct-q4-k-m",
      "display_name": "Llama 1.2b",
      "provider": "Meta",
      "family": "Llama",
      "provider_class": "ollama",
      "served_model_tag": "llama3.2:1b-instruct-q4_K_M",
      "trust": "sovereign",
      "quantization": "Q4_K_M",
      "parameter_count": 1200000000,
      "estimated_model_vram_mib": 604,
      "estimated_kv_cache_mib": 256
    }
  },
  {
    "id": "heterogeneous-0002",
    "display_name": "Heterogeneous 0002",
    "description": "Random 3-model formation: Qwen 2 + Phi 3 + LFM2",
    "kind": "heterogeneous",
    "max_vram_mib": 12288,
    "primary": {
      "variant_id": "deepseek-r1-7b",
      "display_name": "Qwen 2 7.6b",
      "provider": "Alibaba",
      "family": "Qwen 2",
      "provider_class": "ollama",
      "served_model_tag": "deepseek-r1:7b",
      "trust": "sovereign",
      "quantization": "Q4_K_M",
      "parameter_count": 7600000000,
      "estimated_model_vram_mib": 3826,
      "estimated_kv_cache_mib": 512
    },
    "assistant": {
      "variant_id": "phi4-mini-reasoning-3-8b",
      "display_name": "Phi 3 3.8b",
      "provider": "Microsoft",
      "family": "Phi 3",
      "provider_class": "ollama",
      "served_model_tag": "phi4-mini-reasoning:3.8b",
      "trust": "sovereign",
      "quantization": "Q4_K_M",
      "parameter_count": 3800000000,
      "estimated_model_vram_mib": 1913,
      "estimated_kv_cache_mib": 512
    },
    "lite": {
      "variant_id": "sam860-lfm2-700m",
      "display_name": "LFM2 742m",
      "provider": "LFM",
      "family": "LFM2",
      "provider_class": "ollama",
      "served_model_tag": "sam860/LFM2:700m",
      "trust": "sovereign",
      "quantization": "Q8_0",
      "parameter_count": 742000000,
      "estimated_model_vram_mib": 770,
      "estimated_kv_cache_mib": 256
    }
  },
  {
    "id": "heterogeneous-0003",
    "display_name": "Heterogeneous 0003",
    "description": "Random 3-model formation: GLM 4 + Qwen 3.5 + Llama",
    "kind": "heterogeneous",
    "max_vram_mib": 12288,
    "primary": {
      "variant_id": "milkey-glm-4-9b-0414-q4-k-m",
      "display_name": "GLM 4 9.4b",
      "provider": "GLM",
      "family": "GLM 4",
      "provider_class": "ollama",
      "served_model_tag": "milkey/GLM-4-9B-0414:Q4_K_M",
      "trust": "sovereign",
      "quantization": "Q4_K_M",
      "parameter_count": 9400000000,
      "estimated_model_vram_mib": 4733,
      "estimated_kv_cache_mib": 512
    },
    "assistant": {
      "variant_id": "hf-co-unsloth-qwen3-5-4b-gguf-q4-k-m",
      "display_name": "Qwen 3.5 4.2b",
      "provider": "Alibaba",
      "family": "Qwen 3.5",
      "provider_class": "ollama",
      "served_model_tag": "hf.co/unsloth/Qwen3.5-4B-GGUF:Q4_K_M",
      "trust": "sovereign",
      "quantization": "Q4_K_M",
      "parameter_count": 4200000000,
      "estimated_model_vram_mib": 2119,
      "estimated_kv_cache_mib": 512
    },
    "lite": {
      "variant_id": "llama3-2-1b",
      "display_name": "Llama 1.2b",
      "provider": "Meta",
      "family": "Llama",
      "provider_class": "ollama",
      "served_model_tag": "llama3.2:1b",
      "trust": "sovereign",
      "quantization": "Q8_0",
      "parameter_count": 1200000000,
      "estimated_model_vram_mib": 1246,
      "estimated_kv_cache_mib": 256
    }
  },
  {
    "id": "heterogeneous-0004",
    "display_name": "Heterogeneous 0004",
    "description": "Random 3-model formation: Llama + Qwen 2 + Gemma 3",
    "kind": "heterogeneous",
    "max_vram_mib": 12288,
    "primary": {
      "variant_id": "hf-co-nekuromento-toolace-8b-q8-0-gguf-latest",
      "display_name": "Llama 8.0b",
      "provider": "Meta",
      "family": "Llama",
      "provider_class": "ollama",
      "served_model_tag": "hf.co/Nekuromento/ToolACE-8B-Q8_0-GGUF:latest",
      "trust": "sovereign",
      "quantization": "Q8_0",
      "parameter_count": 8000000000,
      "estimated_model_vram_mib": 8339,
      "estimated_kv_cache_mib": 512
    },
    "assistant": {
      "variant_id": "qwen2-5-1-5b-instruct-q4-k-m",
      "display_name": "Qwen 2 1.5b",
      "provider": "Alibaba",
      "family": "Qwen 2",
      "provider_class": "ollama",
      "served_model_tag": "qwen2.5:1.5b-instruct-q4_K_M",
      "trust": "sovereign",
      "quantization": "Q4_K_M",
      "parameter_count": 1500000000,
      "estimated_model_vram_mib": 755,
      "estimated_kv_cache_mib": 512
    },
    "lite": {
      "variant_id": "gemma3-1b",
      "display_name": "Gemma 3 1000m",
      "provider": "Google",
      "family": "Gemma 3",
      "provider_class": "ollama",
      "served_model_tag": "gemma3:1b",
      "trust": "sovereign",
      "quantization": "Q4_K_M",
      "parameter_count": 1000000000,
      "estimated_model_vram_mib": 503,
      "estimated_kv_cache_mib": 256
    }
  },
  {
    "id": "heterogeneous-0005",
    "display_name": "Heterogeneous 0005",
    "description": "Random 3-model formation: Granite + Qwen 3 + Gemma 3",
    "kind": "heterogeneous",
    "max_vram_mib": 12288,
    "primary": {
      "variant_id": "granite4-2-8b",
      "display_name": "Granite 8.8b",
      "provider": "IBM",
      "family": "Granite",
      "provider_class": "ollama",
      "served_model_tag": "granite4.2:8b",
      "trust": "sovereign",
      "quantization": "Q4_K_M",
      "parameter_count": 8800000000,
      "estimated_model_vram_mib": 4426,
      "estimated_kv_cache_mib": 512
    },
    "assistant": {
      "variant_id": "qwen3-4b",
      "display_name": "Qwen 3 4.0b",
      "provider": "Alibaba",
      "family": "Qwen 3",
      "provider_class": "ollama",
      "served_model_tag": "qwen3:4b",
      "trust": "sovereign",
      "quantization": "Q4_K_M",
      "parameter_count": 4000000000,
      "estimated_model_vram_mib": 2014,
      "estimated_kv_cache_mib": 512
    },
    "lite": {
      "variant_id": "gemma3-270m",
      "display_name": "Gemma 3 268m",
      "provider": "Google",
      "family": "Gemma 3",
      "provider_class": "ollama",
      "served_model_tag": "gemma3:270m",
      "trust": "sovereign",
      "quantization": "Q8_0",
      "parameter_count": 268000000,
      "estimated_model_vram_mib": 278,
      "estimated_kv_cache_mib": 256
    }
  },
  {
    "id": "heterogeneous-0006",
    "display_name": "Heterogeneous 0006",
    "description": "Random 3-model formation: Llama + SmolLM 3 + Qwen 3.5",
    "kind": "heterogeneous",
    "max_vram_mib": 12288,
    "primary": {
      "variant_id": "kitsonk-watt-tool-8b-latest",
      "display_name": "Llama 8.0b",
      "provider": "Meta",
      "family": "Llama",
      "provider_class": "ollama",
      "served_model_tag": "kitsonk/watt-tool-8B:latest",
      "trust": "sovereign",
      "quantization": "Q4_K_M",
      "parameter_count": 8000000000,
      "estimated_model_vram_mib": 4028,
      "estimated_kv_cache_mib": 512
    },
    "assistant": {
      "variant_id": "impulse2000-smollm3-latest",
      "display_name": "SmolLM 3 3.1b",
      "provider": "Impulse2000",
      "family": "SmolLM 3",
      "provider_class": "ollama",
      "served_model_tag": "Impulse2000/smollm3:latest",
      "trust": "sovereign",
      "quantization": "Q4_K_M",
      "parameter_count": 3100000000,
      "estimated_model_vram_mib": 1560,
      "estimated_kv_cache_mib": 512
    },
    "lite": {
      "variant_id": "hf-co-unsloth-qwen3-5-0-8b-gguf-q4-k-m",
      "display_name": "Qwen 3.5 752m",
      "provider": "Alibaba",
      "family": "Qwen 3.5",
      "provider_class": "ollama",
      "served_model_tag": "hf.co/unsloth/Qwen3.5-0.8B-GGUF:Q4_K_M",
      "trust": "sovereign",
      "quantization": "Q4_K_M",
      "parameter_count": 752000000,
      "estimated_model_vram_mib": 378,
      "estimated_kv_cache_mib": 256
    }
  },
  {
    "id": "heterogeneous-0007",
    "display_name": "Heterogeneous 0007",
    "description": "Random 3-model formation: Granite + SmolLM 3 + Llama",
    "kind": "heterogeneous",
    "max_vram_mib": 12288,
    "primary": {
      "variant_id": "hf-co-ibm-granite-granite-4-2-8b-gguf-q4-k-m",
      "display_name": "Granite 8.8b",
      "provider": "IBM",
      "family": "Granite",
      "provider_class": "ollama",
      "served_model_tag": "hf.co/ibm-granite/granite-4.2-8b-GGUF:Q4_K_M",
      "trust": "sovereign",
      "quantization": "Q4_K_M",
      "parameter_count": 8800000000,
      "estimated_model_vram_mib": 4426,
      "estimated_kv_cache_mib": 512
    },
    "assistant": {
      "variant_id": "impulse2000-smollm3-3b-q4-k-m",
      "display_name": "SmolLM 3 3.1b",
      "provider": "Impulse2000",
      "family": "SmolLM 3",
      "provider_class": "ollama",
      "served_model_tag": "Impulse2000/smollm3:3b-q4_k_m",
      "trust": "sovereign",
      "quantization": "Q4_K_M",
      "parameter_count": 3100000000,
      "estimated_model_vram_mib": 1560,
      "estimated_kv_cache_mib": 512
    },
    "lite": {
      "variant_id": "tinyllama-1-1b",
      "display_name": "Llama 1.0b",
      "provider": "Meta",
      "family": "Llama",
      "provider_class": "ollama",
      "served_model_tag": "tinyllama:1.1b",
      "trust": "sovereign",
      "quantization": "Q4_0",
      "parameter_count": 1000000000,
      "estimated_model_vram_mib": 524,
      "estimated_kv_cache_mib": 256
    }
  },
  {
    "id": "heterogeneous-0008",
    "display_name": "Heterogeneous 0008",
    "description": "Random 3-model formation: Llama + Phi 3 + Qwen 3",
    "kind": "heterogeneous",
    "max_vram_mib": 12288,
    "primary": {
      "variant_id": "hermes3-8b",
      "display_name": "Llama 8.0b",
      "provider": "Meta",
      "family": "Llama",
      "provider_class": "ollama",
      "served_model_tag": "hermes3:8b",
      "trust": "sovereign",
      "quantization": "Q4_0",
      "parameter_count": 8000000000,
      "estimated_model_vram_mib": 4196,
      "estimated_kv_cache_mib": 512
    },
    "assistant": {
      "variant_id": "phi4-mini-latest",
      "display_name": "Phi 3 3.8b",
      "provider": "Microsoft",
      "family": "Phi 3",
      "provider_class": "ollama",
      "served_model_tag": "phi4-mini:latest",
      "trust": "sovereign",
      "quantization": "Q4_K_M",
      "parameter_count": 3800000000,
      "estimated_model_vram_mib": 1913,
      "estimated_kv_cache_mib": 512
    },
    "lite": {
      "variant_id": "qwen3-0-6b",
      "display_name": "Qwen 3 752m",
      "provider": "Alibaba",
      "family": "Qwen 3",
      "provider_class": "ollama",
      "served_model_tag": "qwen3:0.6b",
      "trust": "sovereign",
      "quantization": "Q4_K_M",
      "parameter_count": 752000000,
      "estimated_model_vram_mib": 378,
      "estimated_kv_cache_mib": 256
    }
  },
  {
    "id": "heterogeneous-0009",
    "display_name": "Heterogeneous 0009",
    "description": "Random 3-model formation: GLM 4 + Qwen 2 + LFM2",
    "kind": "heterogeneous",
    "max_vram_mib": 12288,
    "primary": {
      "variant_id": "milkey-glm-4-9b-0414-q4-k-m",
      "display_name": "GLM 4 9.4b",
      "provider": "GLM",
      "family": "GLM 4",
      "provider_class": "ollama",
      "served_model_tag": "milkey/GLM-4-9B-0414:Q4_K_M",
      "trust": "sovereign",
      "quantization": "Q4_K_M",
      "parameter_count": 9400000000,
      "estimated_model_vram_mib": 4733,
      "estimated_kv_cache_mib": 512
    },
    "assistant": {
      "variant_id": "hf-co-salesforce-xlam-2-1b-fc-r-gguf-latest",
      "display_name": "Qwen 2 1.5b",
      "provider": "Alibaba",
      "family": "Qwen 2",
      "provider_class": "ollama",
      "served_model_tag": "hf.co/Salesforce/xLAM-2-1b-fc-r-gguf:latest",
      "trust": "sovereign",
      "quantization": "Q4_K_M",
      "parameter_count": 1500000000,
      "estimated_model_vram_mib": 775,
      "estimated_kv_cache_mib": 512
    },
    "lite": {
      "variant_id": "sam860-lfm2-700m",
      "display_name": "LFM2 742m",
      "provider": "LFM",
      "family": "LFM2",
      "provider_class": "ollama",
      "served_model_tag": "sam860/LFM2:700m",
      "trust": "sovereign",
      "quantization": "Q8_0",
      "parameter_count": 742000000,
      "estimated_model_vram_mib": 770,
      "estimated_kv_cache_mib": 256
    }
  },
  {
    "id": "heterogeneous-0010",
    "display_name": "Heterogeneous 0010",
    "description": "Random 3-model formation: Mistral 3 + Qwen 3.5 + Llama",
    "kind": "heterogeneous",
    "max_vram_mib": 12288,
    "primary": {
      "variant_id": "ministral-3-8b",
      "display_name": "Mistral 3 8.9b",
      "provider": "Mistral",
      "family": "Mistral 3",
      "provider_class": "ollama",
      "served_model_tag": "ministral-3:8b",
      "trust": "sovereign",
      "quantization": "Q4_K_M",
      "parameter_count": 8900000000,
      "estimated_model_vram_mib": 4481,
      "estimated_kv_cache_mib": 512
    },
    "assistant": {
      "variant_id": "qwen3-5-4b",
      "display_name": "Qwen 3.5 4.2b",
      "provider": "Alibaba",
      "family": "Qwen 3.5",
      "provider_class": "ollama",
      "served_model_tag": "qwen3.5:4b",
      "trust": "sovereign",
      "quantization": "Q4_K_M",
      "parameter_count": 4200000000,
      "estimated_model_vram_mib": 2119,
      "estimated_kv_cache_mib": 512
    },
    "lite": {
      "variant_id": "tinyllama-1-1b",
      "display_name": "Llama 1.0b",
      "provider": "Meta",
      "family": "Llama",
      "provider_class": "ollama",
      "served_model_tag": "tinyllama:1.1b",
      "trust": "sovereign",
      "quantization": "Q4_0",
      "parameter_count": 1000000000,
      "estimated_model_vram_mib": 524,
      "estimated_kv_cache_mib": 256
    }
  },
  {
    "id": "heterogeneous-0011",
    "display_name": "Heterogeneous 0011",
    "description": "Random 3-model formation: Granite + Qwen 3.5 + Llama",
    "kind": "heterogeneous",
    "max_vram_mib": 12288,
    "primary": {
      "variant_id": "granite3-3-8b",
      "display_name": "Granite 8.2b",
      "provider": "IBM",
      "family": "Granite",
      "provider_class": "ollama",
      "served_model_tag": "granite3.3:8b",
      "trust": "sovereign",
      "quantization": "Q4_K_M",
      "parameter_count": 8200000000,
      "estimated_model_vram_mib": 4129,
      "estimated_kv_cache_mib": 512
    },
    "assistant": {
      "variant_id": "hf-co-unsloth-qwen3-5-2b-gguf-q4-k-m",
      "display_name": "Qwen 3.5 1.9b",
      "provider": "Alibaba",
      "family": "Qwen 3.5",
      "provider_class": "ollama",
      "served_model_tag": "hf.co/unsloth/Qwen3.5-2B-GGUF:Q4_K_M",
      "trust": "sovereign",
      "quantization": "Q4_K_M",
      "parameter_count": 1900000000,
      "estimated_model_vram_mib": 946,
      "estimated_kv_cache_mib": 512
    },
    "lite": {
      "variant_id": "tinyllama-1-1b",
      "display_name": "Llama 1.0b",
      "provider": "Meta",
      "family": "Llama",
      "provider_class": "ollama",
      "served_model_tag": "tinyllama:1.1b",
      "trust": "sovereign",
      "quantization": "Q4_0",
      "parameter_count": 1000000000,
      "estimated_model_vram_mib": 524,
      "estimated_kv_cache_mib": 256
    }
  },
  {
    "id": "heterogeneous-0012",
    "display_name": "Heterogeneous 0012",
    "description": "Random 3-model formation: Qwen 3.5 + SmolLM 3 + Gemma 3",
    "kind": "heterogeneous",
    "max_vram_mib": 12288,
    "primary": {
      "variant_id": "qwen3-5-9b",
      "display_name": "Qwen 3.5 8.9b",
      "provider": "Alibaba",
      "family": "Qwen 3.5",
      "provider_class": "ollama",
      "served_model_tag": "qwen3.5:9b",
      "trust": "sovereign",
      "quantization": "Q4_K_M",
      "parameter_count": 8900000000,
      "estimated_model_vram_mib": 4506,
      "estimated_kv_cache_mib": 512
    },
    "assistant": {
      "variant_id": "impulse2000-smollm3-3b-q4-k-m",
      "display_name": "SmolLM 3 3.1b",
      "provider": "Impulse2000",
      "family": "SmolLM 3",
      "provider_class": "ollama",
      "served_model_tag": "Impulse2000/smollm3:3b-q4_k_m",
      "trust": "sovereign",
      "quantization": "Q4_K_M",
      "parameter_count": 3100000000,
      "estimated_model_vram_mib": 1560,
      "estimated_kv_cache_mib": 512
    },
    "lite": {
      "variant_id": "gemma3-1b",
      "display_name": "Gemma 3 1000m",
      "provider": "Google",
      "family": "Gemma 3",
      "provider_class": "ollama",
      "served_model_tag": "gemma3:1b",
      "trust": "sovereign",
      "quantization": "Q4_K_M",
      "parameter_count": 1000000000,
      "estimated_model_vram_mib": 503,
      "estimated_kv_cache_mib": 256
    }
  },
  {
    "id": "heterogeneous-0013",
    "display_name": "Heterogeneous 0013",
    "description": "Random 3-model formation: Granite + Phi 3 + Llama",
    "kind": "heterogeneous",
    "max_vram_mib": 12288,
    "primary": {
      "variant_id": "granite3-3-8b",
      "display_name": "Granite 8.2b",
      "provider": "IBM",
      "family": "Granite",
      "provider_class": "ollama",
      "served_model_tag": "granite3.3:8b",
      "trust": "sovereign",
      "quantization": "Q4_K_M",
      "parameter_count": 8200000000,
      "estimated_model_vram_mib": 4129,
      "estimated_kv_cache_mib": 512
    },
    "assistant": {
      "variant_id": "phi4-mini-3-8b",
      "display_name": "Phi 3 3.8b",
      "provider": "Microsoft",
      "family": "Phi 3",
      "provider_class": "ollama",
      "served_model_tag": "phi4-mini:3.8b",
      "trust": "sovereign",
      "quantization": "Q4_K_M",
      "parameter_count": 3800000000,
      "estimated_model_vram_mib": 1913,
      "estimated_kv_cache_mib": 512
    },
    "lite": {
      "variant_id": "tinyllama-1-1b",
      "display_name": "Llama 1.0b",
      "provider": "Meta",
      "family": "Llama",
      "provider_class": "ollama",
      "served_model_tag": "tinyllama:1.1b",
      "trust": "sovereign",
      "quantization": "Q4_0",
      "parameter_count": 1000000000,
      "estimated_model_vram_mib": 524,
      "estimated_kv_cache_mib": 256
    }
  },
  {
    "id": "heterogeneous-0014",
    "display_name": "Heterogeneous 0014",
    "description": "Random 3-model formation: Qwen 2 + Phi 3 + Llama",
    "kind": "heterogeneous",
    "max_vram_mib": 12288,
    "primary": {
      "variant_id": "qwen2-5-7b",
      "display_name": "Qwen 2 7.6b",
      "provider": "Alibaba",
      "family": "Qwen 2",
      "provider_class": "ollama",
      "served_model_tag": "qwen2.5:7b",
      "trust": "sovereign",
      "quantization": "Q4_K_M",
      "parameter_count": 7600000000,
      "estimated_model_vram_mib": 3826,
      "estimated_kv_cache_mib": 512
    },
    "assistant": {
      "variant_id": "phi3-5-3-8b-mini-instruct-q4-k-m",
      "display_name": "Phi 3 3.8b",
      "provider": "Microsoft",
      "family": "Phi 3",
      "provider_class": "ollama",
      "served_model_tag": "phi3.5:3.8b-mini-instruct-q4_K_M",
      "trust": "sovereign",
      "quantization": "Q4_K_M",
      "parameter_count": 3800000000,
      "estimated_model_vram_mib": 1913,
      "estimated_kv_cache_mib": 512
    },
    "lite": {
      "variant_id": "llama3-2-1b",
      "display_name": "Llama 1.2b",
      "provider": "Meta",
      "family": "Llama",
      "provider_class": "ollama",
      "served_model_tag": "llama3.2:1b",
      "trust": "sovereign",
      "quantization": "Q8_0",
      "parameter_count": 1200000000,
      "estimated_model_vram_mib": 1246,
      "estimated_kv_cache_mib": 256
    }
  }
];

export const FORMATION_CATALOG: readonly FormationTopologySpec[] = RAW_FORMATIONS.map((f) => {
  const totalVram =
    f.primary.estimated_model_vram_mib +
    f.primary.estimated_kv_cache_mib +
    f.assistant.estimated_model_vram_mib +
    f.assistant.estimated_kv_cache_mib +
    f.lite.estimated_model_vram_mib +
    f.lite.estimated_kv_cache_mib;
  const providers = new Set([f.primary.provider, f.assistant.provider, f.lite.provider]);
  const families = new Set([f.primary.family, f.assistant.family, f.lite.family]);
  return {
    ...f,
    total_vram_mib: totalVram,
    vram_utilization_pct: totalVram / f.max_vram_mib,
    provider_diversity_count: providers.size,
    family_diversity_count: families.size,
  };
});

/** Find all formations where a given model variant participates in any role. */
export function formationsForVariant(variantId: string): Array<{ formation: FormationTopologySpec; role: ModelRole }> {
  const canonical = VARIANT_ALIASES[variantId] ?? variantId;
  const matches: Array<{ formation: FormationTopologySpec; role: ModelRole }> = [];
  for (const formation of FORMATION_CATALOG) {
    for (const role of ["primary", "assistant", "lite"] as const) {
      const binding = formation[role];
      const bindingCanonical = VARIANT_ALIASES[binding.variant_id] ?? binding.variant_id;
      if (binding.variant_id === variantId || bindingCanonical === canonical) {
        matches.push({ formation, role });
      }
    }
  }
  return matches;
}

export const EVALUATION_FRAMEWORK_VALUES = {
  schemaVersion: "execution-topologies.v1",
  verifierContractVersion: "2.0.0",
  scenarioCount: 27,
  scenarioCategories: 10,
  homogeneousRoles: 3,
  homogeneousCellsPerVariant: 75,
  registeredVariantsCount: 94,
  baseCampaignVariantsCount: 75,
  totalFormationsCount: 1005,
  curatedFormationsCount: 5,
  heterogeneousFormationsCount: 1000,
  maxVramBudgetMib: 12288,
  executionOrder: ["lite", "assistant", "primary"] as const,
  pillars: [
    {
      id: "campaigns",
      kicker: "PILLAR 01 · CAMPAIGNS",
      title: "Governed Campaign Programs",
      summary:
        "Three strict evaluation lanes: L1–L5 Native Conformance, 75-cell Homogeneous Model-Role Campaigns (25 scenarios × 3 roles), and 3-Role System Formation Campaigns.",
      metric: "3 Lanes · 75 Cells/Run",
    },
    {
      id: "runs",
      kicker: "PILLAR 02 · RUNS & VERIFICATION",
      title: "Cryptographic Evidence & Verifier v2.0",
      summary:
        "Every assignment emits Ed25519-signed execution receipts and SHA-256 population digests. Non-verdict outcomes (provider, grader, harness) are never conflated with model failures.",
      metric: "Ed25519 + SHA-256",
    },
    {
      id: "formations",
      kicker: "PILLAR 03 · FORMATIONS",
      title: "3-Role Sovereign Execution Topologies",
      summary:
        "1,005 validated 3-model stacks (Lite → Assistant → Primary) enforcing distinct model families, sovereign local execution, and a strict 12,288 MiB (12 GiB) combined VRAM ceiling.",
      metric: "1,005 Stacks · ≤12 GiB VRAM",
    },
    {
      id: "capabilities",
      kicker: "PILLAR 04 · CAPABILITY GATES",
      title: "5-Probe Pre-Flight Capability Matrix",
      summary:
        "Every registered model is probed for Completion, Tool Calling (probe_echo), Structured JSON, Deliberative Thinking, and Context Window before campaign scheduling.",
      metric: "5 Governed Probes",
    },
  ],
} as const;

export interface CampaignSummaryGroup {
  campaign_id: string;
  title: string;
  program: "Governance conformance" | "Homogeneous model campaign" | "System formation campaign";
  arm: string;
  suites: string[];
  runs: EvaluationSummary[];
  total_runs: number;
  completed_runs: number;
  running_runs: number;
  failed_runs: number;
  verified_runs: number;
  verifier_failed_runs: number;
  total_assignments: number;
  completed_assignments: number;
  failed_assignments: number;
  pass_rate_median?: number;
  latest_activity?: string;
}

function inferCampaignGroupKey(run: EvaluationSummary): {
  id: string;
  title: string;
  program: CampaignSummaryGroup["program"];
} {
  if (run.native_result) {
    const id = run.campaign_id || `conformance-${run.suite_id}`;
    return {
      id,
      title: run.campaign_id ? formatCampaignTitle(run.campaign_id) : `Governance Conformance · ${run.suite_id}`,
      program: "Governance conformance",
    };
  }
  if (run.evaluation_unit === "system" || run.arm.includes("heterogeneous") || run.arm.includes("system")) {
    const id = run.campaign_id || `system-${run.suite_id}`;
    return {
      id,
      title: run.campaign_id ? formatCampaignTitle(run.campaign_id) : `System Formation Campaign · ${run.suite_id}`,
      program: "System formation campaign",
    };
  }
  if (run.campaign_id) {
    return {
      id: run.campaign_id,
      title: formatCampaignTitle(run.campaign_id),
      program: "Homogeneous model campaign",
    };
  }
  if (run.run_id.startsWith("eval-init-") || run.suite_id === "eval-init-suite") {
    return {
      id: "eval-genesis-homogeneous",
      title: "Genesis Homogeneous Model-Role Campaign",
      program: "Homogeneous model campaign",
    };
  }
  return {
    id: `campaign-${run.suite_id}-${run.arm}`,
    title: `Model Campaign · ${run.suite_id}`,
    program: "Homogeneous model campaign",
  };
}

function formatCampaignTitle(campaignId: string): string {
  if (campaignId === "eval-genesis-homogeneous") return "Genesis Homogeneous Model-Role Campaign";
  if (campaignId.startsWith("eval-init-")) {
    return `Homogeneous Model Rollout · ${campaignId.replace(/^eval-init-/, "").replace(/-\d+$/, "")}`;
  }
  return campaignId;
}

export function deriveCampaignSummaries(evaluations: EvaluationSummary[]): CampaignSummaryGroup[] {
  const groups = new Map<string, CampaignSummaryGroup>();

  for (const run of evaluations) {
    const { id, title, program } = inferCampaignGroupKey(run);
    let group = groups.get(id);
    if (!group) {
      group = {
        campaign_id: id,
        title,
        program,
        arm: run.arm,
        suites: [],
        runs: [],
        total_runs: 0,
        completed_runs: 0,
        running_runs: 0,
        failed_runs: 0,
        verified_runs: 0,
        verifier_failed_runs: 0,
        total_assignments: 0,
        completed_assignments: 0,
        failed_assignments: 0,
      };
      groups.set(id, group);
    }
    group.runs.push(run);
    if (!group.suites.includes(run.suite_id)) {
      group.suites.push(run.suite_id);
    }
    group.total_runs += 1;
    if (run.lifecycle_state === "completed") group.completed_runs += 1;
    if (run.lifecycle_state === "running" || run.lifecycle_state === "queued") group.running_runs += 1;
    if (run.lifecycle_state === "failed" || run.lifecycle_state === "stopped") group.failed_runs += 1;
    if (run.verifier_state === "passed") group.verified_runs += 1;
    if (run.verifier_state === "failed") group.verifier_failed_runs += 1;
    group.total_assignments += run.assignment_total;
    group.completed_assignments += run.assignment_completed;
    group.failed_assignments += run.assignment_failed;
    const ts = run.started_at ?? run.observed_at;
    if (ts && (!group.latest_activity || ts.localeCompare(group.latest_activity) > 0)) {
      group.latest_activity = ts;
    }
  }

  const result = Array.from(groups.values());
  for (const group of result) {
    const passRates = group.runs
      .map((r) => r.headline_metrics.pass_rate?.value)
      .filter((v): v is number => v !== undefined)
      .sort((a, b) => a - b);
    if (passRates.length > 0) {
      const mid = Math.floor(passRates.length / 2);
      group.pass_rate_median =
        passRates.length % 2 === 1 ? passRates[mid] : (passRates[mid - 1]! + passRates[mid]!) / 2;
    }
  }

  return result.sort((a, b) => (b.latest_activity ?? "").localeCompare(a.latest_activity ?? ""));
}

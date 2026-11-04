// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// Enriched catalog metadata for the Models and Evaluations views:
// - Model hardware specs, clean served tags, family/provider taxonomy, and the 5 governed capability probes
// - Campaign aggregation and Evaluation Framework reference values

import type { EvaluationSummary, ModelSummary } from "../contract/types";
import { evaluationPassRate } from '../utils/run-metrics';

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
    "params": 4210000000,
    "quant": "Q4_K_M",
    "ctx": 262144,
    "caps": "PPPPP"
  },
  "qwen3-5-9b": {
    "tag": "qwen3.5:9b",
    "family": "qwen35",
    "params": 8950000000,
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
  nemotron: { family: "Nemotron", provider: "NVIDIA" },
  deepseek: { family: "DeepSeek R1", provider: "DeepSeek" },
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
  lfm2moe: { family: "LFM2.5 MoE", provider: "Liquid AI" },
  minicpm: { family: "MiniCPM", provider: "OpenBMB" },
  olmo3: { family: "OLMo 3", provider: "Ai2" },
  spark2_5: { family: "Spark X2.6", provider: "SparkLLM" },
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

export interface ResolvedModelProfile {
  variant_id: string;
  served_model_tag?: string;
  family: string;
  family_key: string;
  provider: string;
  parameter_count?: number;
  parameter_label?: string;
  model_digest?: string;
  model_format?: string;
  artifact_size_bytes?: number;
  artifact_label?: string;
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
    | "variant_id"
    | "display_name"
    | "served_model_tag"
    | "parameter_count"
    | "model_digest"
    | "model_family"
    | "model_format"
    | "artifact_size_bytes"
    | "context_limit"
    | "backend_provider_class"
    | "quantization_weight_class"
  >,
): ResolvedModelProfile {
  const canonicalId = VARIANT_ALIASES[model.variant_id] ?? model.variant_id;
  const raw = RAW_MODEL_INVENTORY[canonicalId] ?? RAW_MODEL_INVENTORY[model.variant_id];

  // Provider families describe architecture; named derivatives retain their
  // own identity even when their weights use a Gemma, Llama, or Qwen base.
  const identity = `${model.served_model_tag ?? raw?.tag ?? ""} ${model.variant_id} ${model.display_name}`.toLowerCase();
  const namedFamily = identity.includes("nemotron") ? "nemotron" : identity.includes("deepseek") ? "deepseek" : undefined;
  const familyKey = namedFamily ?? model.model_family ?? raw?.family ?? inferFamilyKey(model.variant_id, model.display_name);
  const familyInfo = FAMILY_LABELS[familyKey] ?? {
    family: capitalizeWords(familyKey),
    provider: model.backend_provider_class ?? "Ollama",
  };

  const servedTag = model.served_model_tag;

  // Only frozen published specifications describe the evaluated deployment.
  // Catalog entries and model names cannot supply missing observations.
  const parameterCount = model.parameter_count || undefined;
  const parameterLabel = formatParameterCount(parameterCount);
  const quantization = model.quantization_weight_class?.toUpperCase() || undefined;
  const contextLimit = model.context_limit || undefined;
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
    family: familyInfo.family,
    family_key: familyKey,
    provider: familyInfo.provider,
    parameter_count: parameterCount,
    parameter_label: parameterLabel,
    model_digest: model.model_digest,
    model_format: model.model_format,
    artifact_size_bytes: model.artifact_size_bytes,
    artifact_label: model.artifact_size_bytes ? `${(model.artifact_size_bytes / 1024 ** 3).toFixed(2)} GiB` : undefined,
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
  if (lower.includes("minicpm")) return "minicpm";
  if (lower.includes("olmo")) return "olmo3";
  if (lower.includes("spark")) return "spark2_5";
  if (lower.includes("lfm")) return "lfm2";
  return "general";
}

function capitalizeWords(input: string): string {
  return input
    .split(/[-_\s]+/)
    .filter(Boolean)
    .map((w) => w.charAt(0).toUpperCase() + w.slice(1))
    .join(" ");
}

export interface CampaignSummaryGroup {
  campaign_id: string;
  title: string;
  program: "Governance conformance" | "Homogeneous model campaign";
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
      .map((r) => evaluationPassRate(r)?.value)
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

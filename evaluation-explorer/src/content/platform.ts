// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// Shared copy for how OpenDevOps.ai deploys and presents ztd.
// Used by the About page and the Docs architecture section.

export const ZTD_REPO_URL = 'https://github.com/zerotrustdata/ztd';

export const GITHUB_SPONSORS_URL = 'https://github.com/sponsors/Badoot';

export const PLATFORM_SITE_URL = 'https://opendevops.ai';

export const PLATFORM_CONTACT_EMAIL = 'danny@lateraluslabs.com';

export const PLATFORM_CONTACT_CALENDLY = 'https://calendly.com/danny-lateraluslabs/quick_discovery';

export const PLATFORM_CONTACT_LINKEDIN = 'https://www.linkedin.com/in/dannybarbour/';

export const SPONSORSHIP_LEDE =
  'OpenDevOps.ai is a fully independent, verifiable LLM benchmarking project. I am completely self-funded and refuse to take venture capital. If this data helps you, please help me keep the servers running and the pipeline unbiased.';

export const SPONSORSHIP_USES = [
  'Keep the public mirror, Cloudflare tunnel, and evaluation pipeline online',
  'Fund GPU time and campaign runs on the home workstation that produces these scores',
  'Preserve editorial independence — no venture capital and no vendor-sponsored benchmarks',
] as const;

export const PLATFORM_SOLO_NOTE = 'Solo operator · home-PC hardware · live pipeline';

export const PLATFORM_OVERVIEW_LEDE =
  'This site is a live deployment of the ztd AI governance suite, not a separate benchmark product. Evaluations run through the governed execution path and publish public-safe results to this Explorer.';

export const ABOUT_SITE_INTRO_BEFORE = 'This Evaluation Explorer is a live window into ';
export const ABOUT_SITE_INTRO_AFTER =
  ' as I develop the platform and run model evaluations through it. You can follow the work here: the models, the tasks, the results, and the available evidence behind them.';

export const ABOUT_PURPOSE_HEADING = 'Building ztd in the open.';
export const ABOUT_EVALUATION_INTRO =
  'These evaluations test the platform and investigate how to make AI systems more useful, accountable, and controlled. Six questions guide the work:';
export const ABOUT_EVALUATION_GOALS = [
  {
    label: 'Does the platform work under real model workloads?',
    detail:
      'Exercise context delivery, tool use, governed execution, and evidence capture together. Use failures to find what needs fixing as ztd develops.',
  },
  {
    label: 'Which models fit the Primary, Assistant, and Lite roles?',
    detail:
      'Identify strong candidates for each job: Primary owns the task, plans, delegates, and synthesizes; Assistant handles bounded technical work; Lite makes constrained decisions or escalates. Evaluate models against the work their role is expected to handle.',
  },
  {
    label: 'How far can small models go with better context?',
    detail:
      'Investigate whether ztd’s context delivery helps small models do useful work. The neuroevolution-inspired idea is that an investigation’s context evolves as evidence accumulates over time. These evaluations ask whether that evolution is actually happening and whether it improves outcomes.',
  },
  {
    label: 'Does a frontier model do better with a ztd team?',
    detail:
      'Compare a frontier model working alone with the same model supported by ztd’s Assistant and Lite models. Look for measurable differences in task outcomes, failure modes, latency, and resource use.',
  },
  {
    label: 'Can we measure accuracy, security, safety, and privacy together?',
    detail:
      'Bring answer correctness, resistance to attacks, safe behavior, and protection of sensitive data into one evaluation program. Report each dimension against the scenarios and evidence that actually test it.',
  },
  {
    label: 'What evidence demonstrates that ztd works?',
    detail:
      'Build inspectable evidence that ztd delivers useful help while enforcing its execution controls. Make successful runs, failures, and limits visible so the claims can be checked.',
  },
] as const;
export const ABOUT_EVIDENCE_INTRO =
  'These questions guide an ongoing investigation. A completed run establishes only what its scenarios and checks cover. Claims about better context or stronger model teams need comparable runs and recorded outcomes.';
export const ABOUT_EVIDENCE_PRINCIPLES = [
  'Follow a result back to its scenario, model role, recorded tool calls, and available trace.',
  'Read evidence verification and task accuracy separately: a signed record supports checking what happened; the scenario’s grading criteria determine what was correct.',
  'Check the published verification status and evidence for each run. The live connection indicator describes the feed connection.',
] as const;
export const ABOUT_PURPOSE =
  'ztd is a zero-trust execution and evidence platform. An AI system can investigate, reason, and propose work; ztd separates that reasoning from the authority to act, so people can get useful help while retaining control of their systems and data.';

export const ABOUT_PRINCIPLES = [
  {
    label: 'Govern the action',
    detail:
      'Bind identity, intent, payload, and state to a verifiable transaction. Enforce policy, with consensus and exact-action human approval when the active posture requires them.',
  },
  {
    label: 'Verify where execution happens',
    detail:
      'The Gateway admits work; the Operator independently verifies it at the managed system. Execution authority and authoritative event receipts stay at the data owner’s boundary.',
  },
  {
    label: 'Show the evidence',
    detail:
      'Signed event receipts record execution attempts and outcomes. Claims follow the evidence: ztd governs its execution path, not tools or side channels that bypass it.',
  },
] as const;

export const ABOUT_ORIGIN_HEADING = 'Why I built it';
export const ABOUT_ORIGIN =
  'During production incidents, customers needed someone to take the problem off their plate: gather context, explain the next step, work inside their controls, verify the fix, and leave a clear record. I wanted people to have that kind of help in their pocket.';

export const ABOUT_HEADING = 'The solo builder';
export const ABOUT_OPENING_BEFORE = "I'm ";
export const ABOUT_OPENING_AFTER =
  ' — founder of Lateralus Labs, principal engineer, and U.S. Navy veteran in Portland, Oregon. I designed, built, and operate ztd as a solo builder.';
export const ABOUT_BACKGROUND =
  'Thirty years in data protection, distributed systems, and production operations shape my work in AI security and agentic infrastructure. I take work from architecture through code, deployment, and incident response, while mentoring engineers and working directly with customers.';

export const ABOUT_EXPERIENCE = [
  {
    company: 'Lateralus Labs · 2025–present',
    text: 'Built ztd end to end: execution governance, identity, agent orchestration, evaluations, APIs, and the operator experience.',
  },
  {
    company: 'Igneous → Rubrik · 2019–2025',
    text: 'Built reliability and support practices from the ground up, carried them through acquisition, and owned service reliability for multi-petabyte NAS Cloud Direct.',
  },
  {
    company: 'Quantum · 2015–2019',
    text: 'Led complex NAS and StorNext investigations; built diagnostic labs and Go automation for production support.',
  },
  {
    company: 'Nike Global Backup · 2000–2015',
    text: 'Engineered enterprise data protection, recovery, automation, and chain-of-custody operations across four service providers.',
  },
  {
    company: 'U.S. Navy · 1996–2000',
    text: 'Commissioned shipboard IT aboard USS Bataan and led UNIX, database, and help-desk operations.',
  },
] as const;

export const ABOUT_AVAILABILITY_HEADING = 'Let’s work together';
export const ABOUT_PORTFOLIO =
  'This is also my working portfolio: a platform I designed, built, and operate, with its development and evaluation evidence open to inspection. I’m looking for a team to join or contracts where I can put that experience to work.';
export const ABOUT_AVAILABILITY =
  'Reach out about ztd licensing, consulting, contract engagements, or full-time W-2 roles.';
export const ABOUT_AVAILABILITY_DETAIL =
  'I can help with AI security and agentic systems, platform engineering and SRE, or distributed data infrastructure — from an ambiguous problem through implementation and production ownership.';

export const ZTD_CORE_DOCS = {
  about: `${ZTD_REPO_URL}/blob/main/docs/core/about.md`,
  position: `${ZTD_REPO_URL}/blob/main/docs/core/position_paper.md`,
} as const;

export const WORKSTATION_SPECS = [
  { label: 'CPU', value: 'Intel Core i9-13900K' },
  { label: 'Memory', value: '64 GB RAM' },
  { label: 'GPU', value: 'NVIDIA GeForce RTX 4070 Ti SUPER · 16 GB VRAM' },
  { label: 'Runtime', value: 'Host binary on Windows · Ollama for local model inference' },
] as const;

export const ZTD_ARCHITECTURE_DOCS = {
  overview: 'https://github.com/zerotrustdata/ztd/blob/main/docs/architecture/overview.md',
  governance: 'https://github.com/zerotrustdata/ztd/blob/main/docs/architecture/governance.md',
  operator: 'https://github.com/zerotrustdata/ztd/blob/main/docs/architecture/operator.md',
  evals: 'https://github.com/zerotrustdata/ztd/blob/main/docs/architecture/evals.md',
} as const;

export const ZTD_DIFFERENTIATORS_LEDE =
  'Most benchmarks score a model API in isolation. ztd scores the full governed execution path — cryptographic proofs, host-bound operators, and the same governed inference path a production workload would traverse.';

export const ZTD_DIFFERENTIATORS = [
  {
    headline: 'Proof, not promises',
    detail:
      'Every governed mutation is a typed, signed, state-bound GovernanceEnvelope. The Gateway admits it; the Operator independently re-verifies hash, nonce, expiry, and posture proofs before any side effect. Signed event receipts and content-addressed evidence land in report.json and verification.json — reproducible offline with ztd eval runs verify, not trust in this browser.',
  },
  {
    headline: 'Five-layer fail-closed governance',
    detail:
      'L1 Doctrine through L5 Actuator form one pipeline: policy admission, consensus and notary gates when required, local Warden verification, and a single Actuator execution boundary. Required proofs fail closed; optional layers remain auditable evidence.',
  },
  {
    headline: 'Outbound-only, no-install operators',
    detail:
      'Remote Operators and Observers are the same static ztd binary — no package install, no inbound management port, no root required. Each session dials out over mTLS, pulls work from the Gateway, and records authoritative evidence in the directory where it was started.',
  },
  {
    headline: 'Independent witnesses, not self-report',
    detail:
      'Scored campaigns enroll separate remote sessions for data execution, governed inference, and provider-boundary observation. The Observer samples GPU and host telemetry at the inference boundary without prompt access or mutation authority — the executor cannot attest its own hardware usage.',
  },
] as const;

/** The complete package every model candidate is measured against. */
export const ZTD_MEASURED_TOGETHER = [
  'A frozen scenario catalog of case inputs, each scored against one registry model in its designated Lite, Assistant, or Primary role',
  'A bounded tool-calling agent loop: every turn is a governed Inference Operator dispatch and every tool call a governed Data Operator action',
  'Governed inference dispatch to Ollama — never a direct provider API shortcut',
  'Provenance attestation of model weights and provider-boundary hardware observation around each inference',
  'Deterministic pass/fail, a sealed agent trace with signed event receipts, and timing when observed',
  'Signed campaign evidence with explicit verified, incomplete, legacy, live, and failed quality states',
] as const;

export const ZTD_CAMPAIGN_OPERATORS = [
  {
    role: 'Gateway (ztdg)',
    wire: 'PDP',
    detail: 'Admits envelopes, enforces L1–L3, routes inference work to bound Operator sessions, coordinates provider-boundary observation and model provenance attestation.',
  },
  {
    role: 'Inference Operator',
    wire: 'ztdo',
    detail: 'Sole scored path to the approved Ollama provider — L4/L5 inference PEP on the campaign host.',
  },
  {
    role: 'Provenance Operator',
    wire: 'ztdo',
    detail: 'Independent storage-side model weight attestor — hashes manifests and blobs at the model storage site and binds digest evidence to inference attempts.',
  },
  {
    role: 'Observer Operator',
    wire: 'ztdo',
    detail: 'Read-only provider-boundary witness on the GPU host — binds hardware samples to inference attempts without mutation authority.',
  },
  {
    role: 'Data Operator',
    wire: 'ztdo',
    detail: 'Governed host boundary bound to each campaign run for filesystem and process actions.',
  },
] as const;

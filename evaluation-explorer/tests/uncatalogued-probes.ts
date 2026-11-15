// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// Synthetic leaderboard fixture variants are absent from the capability probe
// catalog, so they resolve as unprobed and the leaderboard excludes them. Give
// only those variants passing probes; catalogued variants keep their real
// probe results, so each capability-gate test names a catalogued variant.

import type * as FrameworkCatalog from '../src/content/framework-catalog';

export function withPassingProbesForUncatalogued(actual: typeof FrameworkCatalog): typeof FrameworkCatalog {
  return {
    ...actual,
    resolveModelProfile: (model) => {
      const profile = actual.resolveModelProfile(model);
      if (profile.probed_capability_count > 0) return profile;
      const capabilityList = profile.capability_list.map((observation) => ({ ...observation, status: 'pass' as const }));
      return {
        ...profile,
        capabilities: Object.fromEntries(capabilityList.map((observation) => [observation.key, observation])) as typeof profile.capabilities,
        capability_list: capabilityList,
        passed_capability_count: capabilityList.length,
        probed_capability_count: capabilityList.length,
      };
    },
  };
}

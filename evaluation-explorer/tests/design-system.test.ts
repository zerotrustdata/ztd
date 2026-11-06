// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { readFileSync, readdirSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';

const stylesDir = join(__dirname, '../src/styles');
const sheets = readdirSync(stylesDir).filter((name) => name.endsWith('.css'));
const read = (name: string) => readFileSync(join(stylesDir, name), 'utf8');

function matches(pattern: RegExp, names = sheets): string[] {
  return names.flatMap((name) =>
    read(name)
      .split('\n')
      .flatMap((line, index) => (pattern.test(line) ? [`${name}:${index + 1}: ${line.trim()}`] : [])),
  );
}

function themeTokens(selector: string): Record<string, string> {
  const block = read('tokens.css').match(new RegExp(`${selector}\\s*\\{([^}]*)\\}`))?.[1] ?? '';
  return Object.fromEntries([...block.matchAll(/--([\w-]+):\s*(#[0-9a-fA-F]{6})/g)].map((match) => [match[1]!, match[2]!]));
}

function luminance(hex: string): number {
  const [r, g, b] = [1, 3, 5]
    .map((start) => parseInt(hex.slice(start, start + 2), 16) / 255)
    .map((channel) => (channel <= 0.03928 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4));
  return 0.2126 * r! + 0.7152 * g! + 0.0722 * b!;
}

function contrast(left: string, right: string): number {
  const [high, low] = [luminance(left), luminance(right)].sort((a, b) => b - a);
  return (high! + 0.05) / (low! + 0.05);
}

describe.each([
  ['dark', '\\[data-theme="dark"\\]'],
  ['light', '\\[data-theme="light"\\]'],
])('%s theme contrast', (_theme, selector) => {
  const tokens = themeTokens(selector);
  const surfaces = ['bg', 'bg-elev', 'bg-elev2', 'bg-elev3'];

  it.each(['fg', 'fg-muted', 'fg-dim', 'accent', 'accent-dim', 'tone-ok', 'tone-info', 'tone-warn', 'tone-critical', 'tone-neutral'])(
    '%s holds 4.5:1 on every surface',
    (tone) => {
      for (const surface of surfaces) {
        expect(contrast(tokens[tone]!, tokens[surface]!), `${tone} on ${surface}`).toBeGreaterThanOrEqual(4.5);
      }
    },
  );

  it('--border-strong holds 3:1 on every surface, so it can bound a control', () => {
    for (const surface of surfaces) {
      expect(contrast(tokens['border-strong']!, tokens[surface]!), surface).toBeGreaterThanOrEqual(3);
    }
  });
});

describe('explorer design system', () => {
  it('builds hierarchy from hairlines rather than shadows or gradients', () => {
    expect(matches(/box-shadow|text-shadow|backdrop-filter|gradient\(/)).toEqual([]);
  });

  it('keeps corners square outside form controls and the spinner', () => {
    const allowed = /border-radius: (0|50%|var\(--radius-control\))\s*[;}]/;
    expect(matches(/border-radius/).filter((line) => !allowed.test(line))).toEqual([]);
  });

  it('takes every color from a token', () => {
    const literal = /#[0-9a-fA-F]{3,8}\b|rgba?\(/;
    const outsideTokens = sheets.filter((name) => name !== 'tokens.css');
    expect(matches(literal, outsideTokens)).toEqual([]);
  });

  it('does not set type below the 11px label size', () => {
    expect(matches(/font-size: (\d|10)(\.\d+)?px/)).toEqual([]);
  });

  it('keeps tribunal seat text at full strength', () => {
    expect(read('live.css').match(/\.tribunal-seat\s*\{[^}]*opacity/)).toBeNull();
  });

  it('moves nothing on hover', () => {
    const rules = sheets.flatMap((name) => [...read(name).matchAll(/([^{}]*:hover[^{}]*)\{([^{}]*)\}/g)]);
    expect(rules.filter((rule) => /transform/.test(rule[2]!)).map((rule) => rule[1]!.trim())).toEqual([]);
  });
});

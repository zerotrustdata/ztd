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
    // The light theme's logo nameplate is the one fixed surface: the artwork's wordmark is near-white.
    expect(matches(literal, outsideTokens).filter((line) => !line.includes('background: #0c0c0b'))).toEqual([]);
  });

  it('does not set type below the 11px label size', () => {
    expect(matches(/font-size: (\d|10)(\.\d+)?px/)).toEqual([]);
  });

  it('moves nothing on hover', () => {
    const rules = sheets.flatMap((name) => [...read(name).matchAll(/([^{}]*:hover[^{}]*)\{([^{}]*)\}/g)]);
    expect(rules.filter((rule) => /transform/.test(rule[2]!)).map((rule) => rule[1]!.trim())).toEqual([]);
  });
});

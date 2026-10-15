// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { readFile } from 'node:fs/promises';
import { renderMarkdownBody } from './journal.js';
import { OEM_TERMS_PATH, TERMS_PATH, escape, layout } from './layout.js';

// Legal pages buyers review before checkout. The Stripe account's Terms of Service URL points at TERMS_PATH; the
// OEM page publishes the agreement template that OEM buyers sign after checkout.
export const TERMS_PAGES = [
  {
    path: TERMS_PATH,
    source: new URL('../content/terms.md', import.meta.url),
    title: 'Commercial License and Services Terms',
    description: 'Terms for ztd commercial license plans and services engagements purchased from Lateralus Labs, LLC.'
  },
  {
    path: OEM_TERMS_PATH,
    source: new URL('../../legal/oem-license-agreement.md', import.meta.url),
    title: 'ztd Commercial OEM License Agreement',
    description: 'The agreement template OEM buyers sign after checkout to embed and redistribute ztd in their products.'
  }
];

export function renderTerms(markdown, { path, title, description }) {
  const normalized = markdown.replace(/\r\n/g, '\n').trim();
  if (!normalized.startsWith(`# ${title}\n`)) throw new Error(`${path} Markdown H1 must match its title`);
  const { html } = renderMarkdownBody(normalized.slice(normalized.indexOf('\n') + 1).trim());
  return layout({ title, description, path, content: `
<article><header class="article-header"><h1>${escape(title)}</h1><p class="article-deck">${escape(description)}</p></header>
<div class="article-body">${html}</div></article>` });
}

export async function prepareTerms(pages = TERMS_PAGES) {
  return new Map(await Promise.all(pages.map(async page =>
    [`${page.path.slice(1)}index.html`, renderTerms(await readFile(page.source, 'utf8'), page)])));
}

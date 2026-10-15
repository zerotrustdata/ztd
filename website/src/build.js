// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { cp, mkdir, readFile, rm, writeFile } from 'node:fs/promises';
import { dirname, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { prepareJournal } from './journal.js';
import { validatePaymentLinks } from './payments.js';
import { prepareReleaseNotes } from './release-notes.js';
import { prepareTerms } from './terms.js';

const SOURCE_DIRECTORY = dirname(fileURLToPath(import.meta.url));
const WEBSITE_DIRECTORY = resolve(SOURCE_DIRECTORY, '..');
const REPOSITORY_DIRECTORY = resolve(WEBSITE_DIRECTORY, '..');
const OUTPUT_DIRECTORY = resolve(WEBSITE_DIRECTORY, 'dist');
const STATIC_DIRECTORY = resolve(WEBSITE_DIRECTORY, 'static');
const README_PATH = resolve(REPOSITORY_DIRECTORY, 'README.md');
const HOMEPAGE_PATH = resolve(SOURCE_DIRECTORY, 'index.html');
const STYLE_ELEMENT = /<style>([\s\S]*?)<\/style>/;
const SCRIPT_ELEMENT = /<script>([\s\S]*?)<\/script>/;
const LEGACY_BRAND = /g8e/i;

export function prepareHomepage(source) {
  const style = source.match(STYLE_ELEMENT);
  const script = source.match(SCRIPT_ELEMENT);
  if (!style || !script) throw new Error('homepage must contain one inline style and one inline script');

  const html = source
    .replace(STYLE_ELEMENT, '<link rel="stylesheet" href="/styles.css">')
    .replace(SCRIPT_ELEMENT, '<script src="/site.js" defer></script>');

  return {
    html,
    styles: `${style[1].trim()}\n`,
    script: `${script[1].trim()}\n`
  };
}

export function validateHomepage({ html, styles, script }) {
  const output = `${html}\n${styles}\n${script}`;
  if (
    LEGACY_BRAND.test(output) ||
    !html.includes('<link rel="canonical" href="https://zerotrustdata.io/">') ||
    !html.includes('https://github.com/zerotrustdata/ztd') ||
    !html.includes('class="wordmark"') ||
    !html.includes('id="pipeline"') ||
    !html.includes('id="start"') ||
    !html.includes('href="/styles.css"') ||
    !html.includes('src="/site.js"') ||
    /<style\b/i.test(html) ||
    /<script>([\s\S]*?)<\/script>/i.test(html) ||
    /\sstyle=/i.test(html)
  ) throw new Error('generated website validation failed');
}

async function build() {
  const [source, readme] = await Promise.all([
    readFile(HOMEPAGE_PATH, 'utf8'),
    readFile(README_PATH, 'utf8')
  ]);
  const homepage = prepareHomepage(source);
  validateHomepage(homepage);
  validatePaymentLinks(homepage.html);
  const [journal, releaseNotes, terms] = await Promise.all([prepareJournal(), prepareReleaseNotes(REPOSITORY_DIRECTORY), prepareTerms()]);
  if (process.argv.includes('--check')) return;

  await rm(OUTPUT_DIRECTORY, { recursive: true, force: true });
  await mkdir(OUTPUT_DIRECTORY, { recursive: true });
  await cp(STATIC_DIRECTORY, OUTPUT_DIRECTORY, { recursive: true });
  for (const [path, content] of [...journal, ...releaseNotes, ...terms]) {
    const target = resolve(OUTPUT_DIRECTORY, path);
    await mkdir(dirname(target), { recursive: true });
    await writeFile(target, content);
  }
  await Promise.all([
    writeFile(resolve(OUTPUT_DIRECTORY, 'index.html'), homepage.html),
    writeFile(resolve(OUTPUT_DIRECTORY, 'styles.css'), homepage.styles),
    writeFile(resolve(OUTPUT_DIRECTORY, 'site.js'), homepage.script),
    writeFile(resolve(OUTPUT_DIRECTORY, 'llms-full.txt'), readme),
    writeFile(resolve(OUTPUT_DIRECTORY, 'llms.txt'), '# ztd: a zero-trust execution layer for AI agents\n\nThe canonical project overview is available at https://zerotrustdata.io/llms-full.txt and the source repository is https://github.com/zerotrustdata/ztd.\n')
  ]);
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  build().catch(error => {
    process.stderr.write(`${error.message}\n`);
    process.exitCode = 1;
  });
}

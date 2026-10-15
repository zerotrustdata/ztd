// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// Shared page shell for the journal (rendered at build time) and the Releases pages (rendered by the Worker).
// This module must stay free of Node.js imports because worker.js bundles it.

export const ORIGIN = 'https://zerotrustdata.io';
export const JOURNAL_PATH = '/build-in-public/';
export const RELEASES_PATH = '/releases/';
export const TERMS_PATH = '/terms/';
export const OEM_TERMS_PATH = '/terms/oem/';
export const REPOSITORY_URL = 'https://github.com/zerotrustdata/ztd';

export const escape = value => String(value).replace(/[&<>"']/g, char => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[char]);

const NAV = [
  { section: 'journal', href: JOURNAL_PATH, label: 'Build in Public' },
  { section: 'releases', href: RELEASES_PATH, label: 'Releases' }
];

// section names the primary navigation entry for this page; current marks it as the page itself (index pages).
export function layout({ title, description, path, section, current = false, article, content }) {
  const nav = NAV.map(item => item.section === section
    ? `<a class="nav-current" href="${item.href}"${current ? ' aria-current="page"' : ''}>${item.label}</a>`
    : `<a href="${item.href}">${item.label}</a>`).join('');
  return `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>${escape(title)} — Zero Trust Data</title>
<meta name="description" content="${escape(description)}">
<meta name="theme-color" content="#080b10">
<link rel="canonical" href="${ORIGIN}${path}">
<meta property="og:type" content="${article ? 'article' : 'website'}">
<meta property="og:title" content="${escape(title)}">
<meta property="og:description" content="${escape(description)}">
<meta property="og:url" content="${ORIGIN}${path}">
<meta property="og:site_name" content="Zero Trust Data">
<meta name="twitter:card" content="summary">
${article ? `<meta name="author" content="${escape(article.author)}"><meta property="article:published_time" content="${article.date}">` : ''}
<link rel="icon" href="/favicon-100x100.png" type="image/png" sizes="100x100">
<link rel="alternate" type="application/rss+xml" title="Build in Public — Zero Trust Data" href="${JOURNAL_PATH}feed.xml">
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Chakra+Petch:wght@500;600;700&family=Inter:wght@400;500;600;700;800&family=JetBrains+Mono:wght@400;500&display=swap" rel="stylesheet">
<link rel="stylesheet" href="/journal.css">
<script src="/journal.js" defer></script>
</head>
<body>
<a class="skip" href="#main">Skip to content</a>
<header class="site-header"><div class="shell header-inner">
<a class="brand" href="/" aria-label="Zero Trust Data home"><img src="/logo.png" width="74" height="32" alt=""><span>ZERO TRUST DATA</span></a>
<nav aria-label="Primary"><a href="/#how">The platform</a>${nav}<a class="github-link" href="${REPOSITORY_URL}">GitHub <span aria-hidden="true">↗</span></a></nav>
</div></header>
${article ? '<progress class="reading-progress" max="100" value="0" aria-label="Reading progress" hidden></progress>' : ''}
<main id="main" class="shell">${content}</main>
<footer class="site-footer shell"><a class="footer-brand" href="/">Zero Trust Data<span>A model can ask. The host decides.</span></a><div><a href="${RELEASES_PATH}">Releases ↗</a><a href="${JOURNAL_PATH}feed.xml">RSS feed ↗</a><a href="mailto:danny@zerotrustdata.io">Get in touch ↗</a><a href="${TERMS_PATH}">Terms ↗</a><a href="${REPOSITORY_URL}">Source ↗</a></div><p>© 2026 Lateralus Labs, LLC.</p></footer>
</body>
</html>\n`;
}

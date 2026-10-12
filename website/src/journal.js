// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import { readFile } from 'node:fs/promises';
import { Marked, Renderer } from 'marked';

const ORIGIN = 'https://zerotrustdata.io';
export const JOURNAL_PATH = '/build-in-public/';
const DESCRIPTION = 'Building Zero Trust Data in public. Daily notes on the code, the decisions, the failures, and the evidence behind ztd.';
const escape = value => String(value).replace(/[&<>"']/g, char => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[char]);
const postPath = post => `${JOURNAL_PATH}${post.slug}/`;
const dateLabel = date => new Intl.DateTimeFormat('en-US', { month: 'short', day: 'numeric', year: 'numeric', timeZone: 'UTC' }).format(new Date(`${date}T12:00:00Z`));
const tags = post => post.tags.map(tag => `<span class="tag">${escape(tag)}</span>`).join('');
const meta = post => `<time datetime="${post.date}">${dateLabel(post.date)}</time><span aria-hidden="true">/</span><span>${post.minutes} min read</span>`;

export function validatePosts(posts) {
  if (!Array.isArray(posts) || !posts.length) throw new Error('journal requires at least one published post');
  const slugs = new Set();
  for (const post of posts) {
    if (!post || typeof post.slug !== 'string' || !/^[a-z0-9]+(?:-[a-z0-9]+)*$/.test(post.slug) || slugs.has(post.slug)) throw new Error('journal post slug is invalid or duplicated');
    slugs.add(post.slug);
    for (const key of ['title', 'description', 'author']) {
      if (typeof post[key] !== 'string' || !post[key].trim()) throw new Error(`journal post ${post.slug} requires ${key}`);
    }
    if (typeof post.date !== 'string' || !/^\d{4}-\d{2}-\d{2}$/.test(post.date) || !Number.isFinite(Date.parse(post.date)) || new Date(post.date).toISOString().slice(0, 10) !== post.date) throw new Error(`journal post ${post.slug} has an invalid date`);
    if (!Array.isArray(post.tags) || !post.tags.length || post.tags.some(tag => typeof tag !== 'string' || !tag.trim()) || new Set(post.tags).size !== post.tags.length) throw new Error(`journal post ${post.slug} requires unique tags`);
  }
}

function safeLink(href) {
  if (/^(?:https?:\/\/|mailto:|#|\/(?!\/))/i.test(href)) return href;
  throw new Error(`journal link must be a public URL, root-relative path, or anchor: ${href}`);
}

export function renderMarkdown(markdown, title) {
  const normalized = markdown.replace(/\r\n/g, '\n').trim();
  if (!normalized.startsWith(`# ${title}\n`)) throw new Error('journal Markdown H1 must match the post title');
  const body = normalized.slice(normalized.indexOf('\n') + 1).trim();
  const headings = [];
  const ids = new Set();
  const renderer = new Renderer();
  const defaultTable = renderer.table;
  renderer.html = ({ text }) => escape(text);
  renderer.heading = function ({ tokens, depth, text }) {
    if (depth === 1) throw new Error('journal body headings must start at H2');
    const base = text.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '') || 'section';
    let id = `section-${base}`;
    for (let n = 2; ids.has(id); n++) id = `section-${base}-${n}`;
    ids.add(id);
    const label = this.parser.parseInline(tokens);
    if (depth === 2) headings.push({ id, label });
    return `<h${depth} id="${id}">${label}</h${depth}>\n`;
  };
  renderer.link = function ({ href, title: label, tokens }) {
    return `<a href="${escape(safeLink(href))}"${label ? ` title="${escape(label)}"` : ''}>${this.parser.parseInline(tokens)}</a>`;
  };
  renderer.image = ({ href, text }) => `<img src="${escape(safeLink(href))}" alt="${escape(text)}" loading="lazy" decoding="async">`;
  renderer.table = function (token) {
    return `<div class="table-scroll" role="region" aria-label="Data table" tabindex="0">${defaultTable.call(this, token)}</div>\n`;
  };
  // Use CSS classes instead of Marked's inline table alignment styles for the strict CSP.
  renderer.tablecell = function ({ tokens, header, align }) {
    const cell = header ? 'th' : 'td';
    return `<${cell}${header ? ' scope="col"' : ''}${align ? ` class="align-${align}"` : ''}>${this.parser.parseInline(tokens)}</${cell}>\n`;
  };
  const html = new Marked({ renderer, gfm: true }).parse(body);
  return { html, headings, minutes: Math.max(1, Math.ceil(body.split(/\s+/).length / 220)) };
}

function layout({ title, description, path, article, content }) {
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
<nav aria-label="Primary"><a href="/#how">The platform</a><a class="nav-current" href="${JOURNAL_PATH}"${article ? '' : ' aria-current="page"'}>Build in Public</a><a class="github-link" href="https://github.com/zerotrustdata/ztd">GitHub <span aria-hidden="true">↗</span></a></nav>
</div></header>
${article ? '<progress class="reading-progress" max="100" value="0" aria-label="Reading progress" hidden></progress>' : ''}
<main id="main" class="shell">${content}</main>
<footer class="site-footer shell"><a class="footer-brand" href="/">Zero Trust Data<span>A model can ask. The host decides.</span></a><div><a href="${JOURNAL_PATH}feed.xml">RSS feed ↗</a><a href="mailto:danny@zerotrustdata.io">Get in touch ↗</a><a href="https://github.com/zerotrustdata/ztd">Source ↗</a></div><p>© 2026 Lateralus Labs, LLC.</p></footer>
</body>
</html>\n`;
}

function postCard(post, index, total) {
  const featured = index === 0;
  return `<article class="post-card${featured ? ' featured' : ''}" data-post data-search="${escape(`${post.title} ${post.description} ${post.tags.join(' ')}`.toLowerCase())}" data-tags="${escape(JSON.stringify(post.tags))}">
<div class="post-copy"><div class="post-topline"><span class="entry-number">ENTRY ${String(total - index).padStart(3, '0')}</span>${featured ? '<span class="latest-label">Latest dispatch</span>' : ''}</div>
<div class="tags">${tags(post)}</div><h3><a href="${postPath(post)}">${escape(post.title)}</a></h3><p>${escape(post.description)}</p>
<div class="post-meta">${meta(post)}</div><a class="read-link" href="${postPath(post)}">Read the field notes <span aria-hidden="true">↗</span><span class="sr-only">: ${escape(post.title)}</span></a></div>
${featured ? `<a class="post-art" href="${postPath(post)}" aria-label="Read ${escape(post.title)}" tabindex="-1"><img src="/journal-network.svg" width="560" height="480" alt="" fetchpriority="high"><span class="art-caption"><span>INTENT → VERIFICATION → EXECUTION</span><span>ztd / field notes</span></span></a>` : ''}
</article>`;
}

export function renderIndex(posts) {
  const topics = [...new Set(posts.flatMap(post => post.tags))];
  return layout({ title: 'Build in Public', description: DESCRIPTION, path: JOURNAL_PATH, content: `
<section class="journal-hero" aria-labelledby="journal-title"><div class="hero-kicker"><span class="status-dot" aria-hidden="true"></span>THE ENGINEERING JOURNAL<span class="hero-edition">EST. 2026 / ALWAYS IN PROGRESS</span></div>
<h1 id="journal-title">Build in <span>public.</span><span class="title-period" aria-hidden="true">_</span></h1>
<div class="hero-bottom"><p>Building trust, one commit at a time.<br><span>The decisions, the dead ends, and the daily work behind Zero Trust Data. Written as it happens.</span></p><a class="text-link" href="${JOURNAL_PATH}feed.xml">Follow the build <span aria-hidden="true">↗</span><small>Subscribe via RSS</small></a></div></section>
<section class="entries" aria-labelledby="entries-title"><div class="section-topline"><h2 id="entries-title">The field notes <span>${String(posts.length).padStart(2, '0')}</span></h2><span>NEWEST FIRST ↓</span></div>
<div class="journal-tools" hidden><div class="filters" role="group" aria-label="Filter posts by topic"><button type="button" data-filter="" aria-pressed="true">All entries</button>${topics.map(topic => `<button type="button" data-filter="${escape(topic)}" aria-pressed="false">${escape(topic)}</button>`).join('')}</div><label class="search"><span class="sr-only">Search entries</span><svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" aria-hidden="true"><circle cx="10" cy="10" r="6"/><path d="m15 15 5 5"/></svg><input type="search" placeholder="Search the journal…" autocomplete="off"></label></div>
<p class="search-status sr-only" role="status" aria-live="polite"></p><div class="post-list">${posts.map((post, index) => postCard(post, index, posts.length)).join('')}</div><div class="empty-state" hidden><h3>No entries found.</h3><p>Try a different search or topic.</p><button type="button" data-reset>Clear filters</button></div></section>
<aside class="author-note"><div class="author-avatar" aria-hidden="true">D<span>↗</span></div><div><p class="label">A NOTE FROM THE BUILDER</p><h2>Open code. Open notebook.</h2><p>I'm Danny. I'm building ztd, a zero-trust execution layer for AI agents. This is where I share the work behind it: what I tried, what broke, and what I learned along the way.</p><a class="text-link" href="mailto:danny@zerotrustdata.io">Say hello <span aria-hidden="true">↗</span></a></div><span class="note-index" aria-hidden="true">[ WORK IN PROGRESS ]</span></aside>` });
}

export function renderArticle(post, nextPost) {
  return layout({ title: post.title, description: post.description, path: postPath(post), article: post, content: `
<div class="article-breadcrumb"><a href="${JOURNAL_PATH}">← All field notes</a><span>BUILD IN PUBLIC / ENGINEERING JOURNAL</span></div>
<article><header class="article-header"><div class="tags">${tags(post)}</div><h1>${escape(post.title)}</h1><p class="article-deck">${escape(post.description)}</p><div class="article-byline"><span class="byline-avatar" aria-hidden="true">${escape(post.author.charAt(0))}</span><span>${escape(post.author)}</span><span class="post-meta">${meta(post)}</span><button type="button" class="copy-link" hidden>Copy link ↗</button><span class="copy-status sr-only" role="status"></span></div></header>
<div class="article-layout"><aside class="contents"><details open><summary>IN THIS FIELD NOTE <span aria-hidden="true">↗</span></summary><nav aria-label="Article sections">${post.headings.map(heading => `<a href="#${heading.id}">${heading.label}</a>`).join('')}</nav></details><a class="contents-back" href="${JOURNAL_PATH}">← Back to the journal</a></aside><div class="article-body">${post.html}<div class="article-end"><span class="label">END OF FIELD NOTE</span><p>The build continues.</p><a href="${JOURNAL_PATH}feed.xml">Follow the next update via RSS ↗</a></div></div></div></article>
<section class="next-entry" aria-label="Continue reading"><span class="label">${nextPost ? 'KEEP READING' : 'FOLLOW THE WORK'}</span><h2>${nextPost ? `<a href="${postPath(nextPost)}">${escape(nextPost.title)} ↗</a>` : 'Every experiment leaves a trail.'}</h2><a class="text-link" href="${JOURNAL_PATH}">Back to all field notes <span aria-hidden="true">↗</span></a></section>` });
}

export function renderFeed(posts) {
  return `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:atom="http://www.w3.org/2005/Atom"><channel>
<title>Build in Public — Zero Trust Data</title><link>${ORIGIN}${JOURNAL_PATH}</link><description>${escape(DESCRIPTION)}</description><language>en-us</language>
<atom:link href="${ORIGIN}${JOURNAL_PATH}feed.xml" rel="self" type="application/rss+xml"/>
${posts.map(post => `<item><title>${escape(post.title)}</title><link>${ORIGIN}${postPath(post)}</link><guid isPermaLink="true">${ORIGIN}${postPath(post)}</guid><pubDate>${new Date(`${post.date}T12:00:00Z`).toUTCString()}</pubDate><description>${escape(post.description)}</description>${post.tags.map(tag => `<category>${escape(tag)}</category>`).join('')}</item>`).join('\n')}
</channel></rss>\n`;
}

export async function prepareJournal(contentDirectory = new URL('../content/', import.meta.url)) {
  const manifest = JSON.parse(await readFile(new URL('posts.json', contentDirectory), 'utf8'));
  validatePosts(manifest);
  const posts = await Promise.all(manifest.map(async post => {
    const markdown = await readFile(new URL(`posts/${post.slug}.md`, contentDirectory), 'utf8');
    return { ...post, ...renderMarkdown(markdown, post.title) };
  }));
  posts.sort((a, b) => b.date.localeCompare(a.date) || a.slug.localeCompare(b.slug));
  const files = new Map([['build-in-public/index.html', renderIndex(posts)], ['build-in-public/feed.xml', renderFeed(posts)]]);
  posts.forEach((post, index) => files.set(`build-in-public/${post.slug}/index.html`, renderArticle(post, posts[index + 1])));
  const urls = ['/', JOURNAL_PATH, ...posts.map(postPath)];
  files.set('sitemap.xml', `<?xml version="1.0" encoding="UTF-8"?>\n<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">\n${urls.map(path => `  <url><loc>${ORIGIN}${path}</loc></url>`).join('\n')}\n</urlset>\n`);
  return files;
}

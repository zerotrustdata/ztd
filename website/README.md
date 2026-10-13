# zerotrustdata.io

The Zero Trust Data website is a Cloudflare Worker with static assets. The Worker is the origin for both
`zerotrustdata.io` and `www.zerotrustdata.io`; the `www` hostname redirects permanently to the apex while preserving
the path and query string.

## Local verification

Use Node.js 22 or newer:

```bash
npm ci
npm run check
npm run build
```

The source of truth for the homepage is `src/index.html`. The build extracts its inline CSS and JavaScript into
`dist/styles.css` and `dist/site.js` so the deployed page can keep a strict Content Security Policy. It also copies
the install scripts and search metadata into `dist/`; the repository root `README.md` becomes
`dist/llms-full.txt`.

The homepage includes a Contact section linked from the primary navigation. It publishes Danny's email,
`danny@zerotrustdata.io`, as a clickable email link in both the Contact section and the footer.

## Build in Public

The journal lives at `/build-in-public/`, with a permanent URL for every post. `src/journal.js` renders the
index, article pages, RSS feed, and sitemap during the existing static build. `static/journal.css` and
`static/journal.js` provide the responsive layout, topic filters, search, reading progress, and copy-link control.
All posts and navigation remain readable without JavaScript. The Worker uses real 404 responses for missing
pages instead of falling back to the homepage.

To publish a daily update:

1. Add `content/posts/<slug>.md`. Start with one `# Title` matching the metadata title; use `##` and `###`
   for sections. GitHub-flavored Markdown supports tables, lists, links, and fenced code. Raw HTML is displayed
   as text. Use public HTTPS URLs, root-relative website paths, or anchors for links, never local draft paths.
2. Add an entry to `content/posts.json`, for example:

   ```json
   {
     "slug": "a-better-enrollment-flow",
     "title": "A better enrollment flow",
     "date": "2026-10-13",
     "description": "What changed today, why it matters, and what comes next.",
     "author": "Danny",
     "tags": ["Engineering"]
   }
   ```

3. Add the post to the documentation catalog in `docs/devs/docs.md`.
4. Run `make website-test` and `make website-build` from the repository root. Review the generated site with
   `npx wrangler dev --local` from `website/`, then publish through the existing `main` deployment workflow.

Only listed posts are published; keep drafts outside the manifest. The date is the publication date, not the
date of the underlying experiment. Posts sort newest first, with the slug breaking same-day ties. The latest
entry is featured automatically; reading times, topic filters, section navigation, `/build-in-public/feed.xml`,
and `/sitemap.xml` are generated from the published content. Keep slugs stable once published.

The first post, [From 936 to 2,000 Operators on one Gateway](content/posts/enrolling-two-thousand-operators.md),
is the publication copy of the October 8–11 field notes. It uses current ztd branding, preserves the original
measurements and limitations, and replaces private editorial references with a public source note. The retained
local scale reports and runtime state are not copied into the website.

For project context and the current architecture, see the [repository overview](../README.md).

## Production deployment

`wrangler.jsonc` declares both hostnames as Cloudflare Worker Custom Domains. On the first authenticated deployment,
Cloudflare creates the required DNS records and manages their TLS certificates. Porkbun remains the registrar; its
nameserver delegation must match the two nameservers assigned to the `zerotrustdata.io` zone in Cloudflare.
The Worker also keeps its `workers.dev` hostname enabled as a deployment preview while DNS changes propagate.

The GitHub Actions workflow validates website changes on pull requests and deploys changes from `main`. The
`production` environment needs these repository or environment secrets:

- `CLOUDFLARE_ACCOUNT_API_TOKEN` — a scoped token that can deploy Workers, manage Worker routes/custom domains,
  read the zone, and use the `ztd-releases` R2 bucket binding.
- `CLOUDFLARE_ACCOUNT_ID` — the Cloudflare account ID that owns the zone and Worker.

The binary release workflow publishes its binaries, signatures, and per-file checksums to the same R2 bucket before
it makes a GitHub release public. Each release is written under an immutable `v<version>/` prefix, and the bucket-root
`latest.json` (`{"version": "X.Y.Z"}`) is promoted last. `/v/<version>/<file>` serves one pinned version, `/latest.json`
serves the pointer, and unversioned routes such as `/ztd-linux-amd64` resolve through the pointer per request. The
install scripts read `latest.json` once and fetch the binary and its checksum from that single version (or from
`ZTD_VERSION` when it is set). The workflow writes through this package's wrangler (`scripts/release-r2.sh`), so
its `release` environment needs `CLOUDFLARE_ACCOUNT_API_TOKEN` (with R2 object read/write on `ztd-releases`) and
`CLOUDFLARE_ACCOUNT_ID`.

For an initial deployment from a trusted workstation:

```bash
npx wrangler login
npm run deploy
```

Do not redirect `g8e.ai` until `https://zerotrustdata.io`, its install scripts, and release endpoints have been
validated in production.

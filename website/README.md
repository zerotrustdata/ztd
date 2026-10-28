# zerotrustdata.io

The Zero Trust Data website is a Cloudflare Worker with static assets. The Worker is the origin for both `zerotrustdata.io` and `www.zerotrustdata.io`; the `www` hostname redirects permanently to the apex while preserving the path and query string.

## Local verification

Use a Node.js release that satisfies the `engines.node` range in `package.json`:

```bash
npm ci
npm run check
npm run build
```

From the repository root, `make website-bundle-check` builds the static assets before validating the Worker with `wrangler deploy --dry-run`. It works without an existing `website/dist` directory.

The homepage markup and CSS live in `src/index.html`; its behavior lives in `src/site.js`, kept separate so a markup redesign cannot silently replace it. The build extracts the inline CSS into `dist/styles.css` and copies `src/site.js` to `dist/site.js`, so the deployed page keeps a strict Content Security Policy. The build fails if `src/index.html` contains an inline script. `test/site.test.js` runs `site.js` against a stub DOM to check that reduced motion slows the hero grid rather than freezing it, and that Windows visitors land on the Windows install tab. The build also copies the install scripts and search metadata into `dist/`; the repository root `README.md` becomes `dist/llms-full.txt`.

The homepage's “Watch it LIVE” button links to `https://opendevops.ai`. The homepage includes a Contact section (`#contact`), linked from the closing call-to-action paragraph. It publishes Danny's email, `danny@zerotrustdata.io`, as a clickable email link in both the Contact section and the footer.

## Payments

Homepage purchase buttons link to `/buy/<sku>`. The Worker answers with a 303 redirect to that SKU's Stripe Payment Link from `src/payment-links.json`, and unknown SKUs return 404. `STRIPE_API_KEY=rk_live_... scripts/stripe-payment-links.sh --live` creates or reconciles the Stripe products, prices, and Payment Links with the Stripe CLI and rewrites that file. Without `--live`, the script runs in Stripe test mode and prints the mapping without writing it. To change a price, edit the script's catalog and run it again. The new price takes over the lookup key, and the old link is deactivated. The script logs each SKU as it goes. If any Stripe call fails, including when the CLI prints text instead of JSON (an expired or missing key), it prints the failing call and the CLI's output, then exits without writing the file. `npm run check` fails if any `/buy/` SKU on the homepage lacks a live-mode link.

Checkout requires buyers to accept the commercial terms at `/terms/`. The build renders that page from `content/terms.md`. It also publishes the OEM agreement template from `../legal/oem-license-agreement.md` at `/terms/oem/`, which the OEM card and the OEM checkout link to. The terms URL must be set as the Terms of Service URL in the Stripe Dashboard (Settings → Business → Public details) for live mode. After changing link settings in the script, bump `LINK_REVISION` and rerun it, which replaces every link.

`test/stripe-payment-links.test.js` runs the script offline against a fake Stripe CLI (`testdata/fake-stripe.js`). It covers creation, idempotent reruns, price changes, revision bumps, API errors, and test mode. After each deploy, `smoke/live.sh` waits until the site answers with the deployed Worker version (the `x-ztd-worker-version` header, from the `CF_VERSION_METADATA` binding), then checks that every homepage `/buy/` SKU redirects to a loading live-mode Payment Link and that both terms pages are served.

## Build in Public

The journal lives at `/build-in-public/`, with a permanent URL for every post. `src/journal.js` renders the index, article pages, RSS feed, and sitemap during the existing static build. `static/journal.css` and `static/journal.js` provide the responsive layout, topic filters, search, reading progress, and copy-link control. All posts and navigation remain readable without JavaScript. The Worker uses real 404 responses for missing pages instead of falling back to the homepage.

To publish a daily update:

1. Add `content/posts/<slug>.md`. Start with one `# Title` matching the metadata title; use `##` and `###` for sections. GitHub-flavored Markdown supports tables, lists, links, and fenced code. Raw HTML is displayed as text. Use public HTTPS URLs, root-relative website paths, or anchors for links, never local draft paths.
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
4. Run `make website-test` and `make website-build` from the repository root. Review the generated site with `npx wrangler dev --local` from `website/`, then publish through the existing `main` deployment workflow.

Only listed posts are published; keep drafts outside the manifest. The date is the publication date, not the date of the underlying experiment. Posts sort newest first, with the slug breaking same-day ties. The latest entry is featured automatically; reading times, topic filters, section navigation, `/build-in-public/feed.xml`, and `/sitemap.xml` are generated from the published content. Keep slugs stable once published.

Post cards can include an optional `artwork` object in the manifest with `src` (a root-relative SVG path), `alt`, and `caption`. Store the SVG in `static/`; artwork stays with its post regardless of which entry is featured. The fleet illustration belongs to the first, 2,000-Operator field note, with separate illustrations for cross-platform onboarding, the eval repository recovery, and the eval profiling note.

The first post, [From 936 to 2,000 Operators on one Gateway](content/posts/enrolling-two-thousand-operators.md), is the publication copy of the October 8–11 field notes. It uses current ztd branding, preserves the original measurements and limitations, and replaces private editorial references with a public source note. The retained local scale reports and runtime state are not copied into the website.

For project context and the current architecture, see the [repository overview](../README.md).

## Releases and one-command onboarding

`/releases/` lists every published ztd version and `/releases/v<version>/` shows one version's downloads, pinned install commands, verification steps, and release notes; `/releases/latest/` redirects to the version named by `latest.json`. The Worker renders these pages per request (`src/releases.js`) from the `ztd-releases` R2 bucket, so a release appears without a website deploy. A version is listed only when its `v<version>/ztd-binaries.json` manifest exists and it is not newer than `latest.json`: `scripts/release-r2.sh` uploads the manifest last, so an incomplete or unpromoted set never appears. The download table comes from the validated manifest; a malformed manifest is a 503, never a partial page. Release notes are rendered at build time from `docs/release_notes/vX.Y.x/vX.Y.Z.md` into `dist/_release-notes/<version>.html` fragments (`src/release-notes.js`), which the Worker composes into the version page and does not serve on their own. Relative links in a note resolve to that file at the release tag on GitHub, or to the Releases page for links to another note. Notes whose version-matching header uses `TBD` instead of a release date are unpublished drafts and are excluded from the generated fragments. Other malformed headers still fail the build; replace `TBD` with the release date when preparing the release.

`static/start.sh` is the one-command onboarding script, `curl -fsSL https://zerotrustdata.io/start.sh | sh`. It downloads `ztd.sh`, installs the checksum-verified binary into `$ZTD_DIR` (default `~/ztd`), and then runs the same steps as `make quickstart` (`scripts/quickstart.sh`): start or reuse a Gateway, sign in or reuse the CLI identity, and run a first governed action. `static/start.ps1` is the Windows equivalent, `irm https://zerotrustdata.io/start.ps1 | iex`: it runs `ztd.ps1` into `$Env:ZTD_DIR` (default `~\ztd`) and the same four steps. Because `iex` runs it inside the caller's session, all its work sits in one script block: it never calls `exit`, restores the caller's location, and a truncated download fails to parse instead of running. `static/ztd.sh` and `static/ztd.ps1` install only the binary. Both shell scripts run entirely from a `main` function called on the last line, so a truncated download executes nothing, and every child reads `/dev/null` so none can consume the piped script.

`test/start.test.js` and `test/install.test.js` run the scripts offline against stub `curl`, `uname`, and `sudo` commands and a fake `ztd` that records its invocations and stdin. The start tests pipe the script into every available POSIX shell (`sh`, `dash`, `busybox sh`, `bash --posix`), cover reuse, every environment variable, each failure remedy, truncated downloads, and run `shellcheck` when it is installed. `test/start-ps1.test.js` runs `irm https://zerotrustdata.io/start.ps1 | iex` in a real `pwsh` session with `Invoke-RestMethod`/`Invoke-WebRequest` replaced by a stub release host, covering the same cases; it is skipped when native `pwsh` is not on `PATH` (or named by `ZTD_TEST_PWSH`). WSL does not auto-select a Windows `pwsh.exe` inherited on `PATH`, because the fixtures use the local POSIX filesystem and environment.

## Production deployment

`wrangler.jsonc` declares both hostnames as Cloudflare Worker Custom Domains. On the first authenticated deployment, Cloudflare creates the required DNS records and manages their TLS certificates. Porkbun remains the registrar; its nameserver delegation must match the two nameservers assigned to the `zerotrustdata.io` zone in Cloudflare. The Worker also keeps its `workers.dev` hostname enabled as a deployment preview while DNS changes propagate.

The GitHub Actions workflow validates website changes on pull requests and deploys changes from `main`. Both this workflow and the binary release workflow read one pair of repository-level secrets; do not add copies to the `production` or `release` environments, because an environment secret shadows the repository one and drifts from it:

- `CLOUDFLARE_ACCOUNT_API_TOKEN` — a scoped token that can deploy Workers, manage Worker routes/custom domains, read the zone, and read/write objects in the `ztd-releases` R2 bucket.
- `CLOUDFLARE_ACCOUNT_ID` — the Cloudflare account ID that owns the zone and Worker.

The binary release workflow publishes its binaries, signatures, and per-file checksums to the same R2 bucket before it makes a GitHub release public. Each release is written under an immutable `v<version>/` prefix, and the bucket-root `latest.json` (`{"version": "X.Y.Z"}`) is promoted last. `/v/<version>/<file>` serves one pinned version, `/latest.json` serves the pointer, and unversioned routes such as `/ztd-linux-amd64` resolve through the pointer per request. The install scripts read `latest.json` once and fetch the binary and its checksum from that single version (or from `ZTD_VERSION` when it is set). The release workflow's `release` environment job reads the same two repository-level secrets, and `scripts/release-r2.sh` (repository root) writes through this package's wrangler.

For an initial deployment from a trusted workstation:

```bash
npx wrangler login
npm run deploy
```

Do not redirect `g8e.ai` until `https://zerotrustdata.io`, its install scripts, and release endpoints have been validated in production.

# Orbit — marketing site

The public landing page for Orbit. Next.js (App Router) + React server components,
CSS Modules with a hand-built token system. No UI framework, no animation library.

> This is the **website-only** branch (`development/landing-page`). The Orbit CLI
> and its Go source live on `main`. Documentation links on the page resolve
> against `main`.

## Develop

```bash
npm install
npm run dev        # http://localhost:3000
```

## Verify

```bash
npm run typecheck  # tsc --noEmit
npm run lint       # next lint
npm run build      # static production build
```

The whole page prerenders to static HTML; the only client-side JS is the copy
buttons and the install tabs.

## Design system

Tokens live in [`app/globals.css`](app/globals.css): white / near-black / neutral
gray, one accent (Docker blue), light + dark via `prefers-color-scheme`. All
diagrams are inline SVG. Update the canonical origin in
[`lib/site.ts`](lib/site.ts) (`site.domain`) before deploying so metadata,
sitemap, robots and OpenGraph URLs are correct.

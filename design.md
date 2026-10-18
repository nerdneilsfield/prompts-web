# Design — Prompt 库

Locked design system for this app (personal Chinese read/write tool). Redesigned in place; component boundaries, DOM roles and business logic are unchanged.

## Genre
modern-minimal (Cobalt register): cool white, one cobalt signal, hairline structure, tight radii.

## Macrostructure family
- App pages (Prompt 库, 优化工作台): Workbench. Left side rail, compact hairline list + detail, original | result comparison.
- No marketing pages, no enrichment, no hero, no footer.

## Theme (all in `web/src/tokens.css`)
- paper `oklch(98.5% 0.004 250)`, paper-2 `oklch(96.2% 0.007 252)`, surface `oklch(99.6% 0.002 250)`
- ink `oklch(24% 0.02 258)`, ink-2 `oklch(34% 0.018 257)`, muted `oklch(46% 0.016 256)`
- rule `oklch(90.5% 0.008 252)`, field (input border) `oklch(62% 0.02 256)`
- accent / focus `oklch(50% 0.19 258)` (cobalt). Used for the primary button, active nav, selected tag, focus ring. Never as a large fill.
- error / success / warn have soft backgrounds and ink-grade text (≥ 6:1).
- diff add / delete are the only hues beyond cobalt and the status colours.

## Typography
- Display: Space Grotesk (variable, latin subset), 600, tight tracking. Headings only; all roman.
- Body: Inter (variable, latin subset) with PingFang SC / Noto Sans SC / Microsoft YaHei for Chinese.
- Mono: JetBrains Mono (variable, latin subset) for prompt text, tags, diff, source snapshots, so `{{变量}}` reads as literal text.
- Fonts are bundled from `@fontsource-variable/*` via `web/src/fonts.css`. No external font host. Chinese glyphs use system CJK faces by design (a full CJK webfont is multi-MB).
- Scale: `--text-xs .75rem` … `--text-xl 1.375rem`. Body 15px.

## Spacing
4-pt-based named scale `--space-3xs … --space-xl`. Control height `--control-h` 2.25rem (2.75rem on coarse pointers).

## Motion
None except a 0.8s spinner on `aria-busy` buttons; disabled under `prefers-reduced-motion`. No reveals, no transitions.

## States
Every button/input has default, hover (hover-capable pointers only), `:focus-visible` (2px cobalt ring, instant), active, disabled (opacity + not-allowed), loading (`aria-busy` spinner), error (`role="alert"` block with red border + soft background, text never colour-only), success (`role="status"` green block).
Input border width is constant across states.

## Copy
Personal-tool Chinese. No metrics, scores, testimonials or invented statistics. Counts shown (tag counts, character counts) are computed from real data.

## What pages MUST share
Tokens, wordmark "Prompt 库", button shape (6px radius), chip shape (6px radius, mono), hairline panels.

## Layout contract
- ≥ 801px: side rail 13rem + content. Library = 18–24rem list + detail. Workbench compare = two equal columns (原文 | 优化结果).
- ≤ 800px: rail becomes a top bar; library and compare stack to one column.
- ≤ 400px: input row buttons go full width.
- Root `overflow-x: clip` on html and body.

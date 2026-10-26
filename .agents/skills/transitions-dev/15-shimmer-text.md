# Shimmer text

## When to use

A loading / "thinking" label that shimmers — streaming status, "Generating…", any in-progress copy that should feel alive without a spinner. Apply the gradient to the text itself and tune `--shimmer-base` / `--shimmer-highlight` per theme.

## HTML usage

```html
<span class="t-shimmer">Planning next moves</span>
```

Pure CSS — no JS, no class toggling. Tune --shimmer-base /
--shimmer-highlight in your own theme rules so the colors
follow light / dark mode.

## Tunable variables

| Variable | Default | Notes |
| --- | --- | --- |
| `--shimmer-dur` | `2000ms` | sourced from `--p15-dur` |
| `--shimmer-base` | `#7c7c7c` | sourced from `--p15-base` |
| `--shimmer-highlight` | `#0d0d0d` | sourced from `--p15-highlight` |
| `--shimmer-band` | `400%` | sourced from `--p15-band` |
| `--shimmer-ease` | `linear` | sourced from `--p15-ease` |

The `:root` defaults below match the live tuning on [transitions.dev](https://transitions.dev). Drop them into your global stylesheet once — every transition in this skill reads from semantic names like these, so multiple transitions can share a single `:root` block.

```css
:root {
  --shimmer-dur: 2000ms;
  --shimmer-base: #7c7c7c;
  --shimmer-highlight: #0d0d0d;
  --shimmer-band: 400%;
  --shimmer-ease: linear;
}
```

## CSS

```css
.t-shimmer {
  display: inline-block;
  background-image: linear-gradient(
    90deg,
    var(--shimmer-base)   0%,
    var(--shimmer-base)  40%,
    var(--shimmer-highlight) 50%,
    var(--shimmer-base)  60%,
    var(--shimmer-base) 100%
  );
  background-size: var(--shimmer-band) 100%;
  background-repeat: no-repeat;
  -webkit-background-clip: text;
  background-clip: text;
  color: transparent;
  -webkit-text-fill-color: transparent;
  animation: t-shimmer var(--shimmer-dur) var(--shimmer-ease) infinite;
}
@keyframes t-shimmer {
  0%   { background-position: 100% 0; }
  100% { background-position: 0% 0; }
}

@media (prefers-reduced-motion: reduce) {
  .t-shimmer { animation: none !important; }
}
```

The `@media (prefers-reduced-motion: reduce)` guard at the bottom of the snippet is required — keep it. It zeroes the transition for users who have asked for less motion at the OS level.

## JavaScript orchestration

None — pure CSS. Toggle the documented HTML attributes or class names from whatever already drives state in your app.


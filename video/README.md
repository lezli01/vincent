# vincent promo video

A ~56 s social promo built with [Remotion](https://www.remotion.dev), in two
cuts: `VincentVertical` (1080×1920, Reels / Shorts / TikTok) and
`VincentLandscape` (1920×1080, X / LinkedIn / YouTube).

```sh
npm install
npm run studio   # live preview
npm run render   # writes out/vincent-vertical.mp4 and out/vincent-landscape.mp4
```

- `src/Promo.tsx` — the timeline: every scene and its length in frames (30 fps).
- `src/tui/` — the TUI recreated in React and driven by scripted keypresses;
  each scene's `*_KEYS` table says when which key is pressed. The colours are
  the TokyoNight theme `scripts/screenshots.sh` photographs the real TUI with.
  These are marketing composites, not captures: documentation images still
  come only from `scripts/screenshots.sh`.
- `scripts/make-music.mjs` — synthesizes the soundtrack into `public/music.wav`
  (generated, not committed; every `npm run` script rebuilds it). It is 120 BPM,
  so one beat is 15 frames. Its arguments are the total length, the drop (the
  logo reveal) and the outro (the repo-link card) in seconds — update them in
  `package.json`'s `music` script when the timeline changes.

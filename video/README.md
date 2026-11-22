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

## Release cuts

`src/release014/` is the 0.14.0 release video (~39 s), in the same two shapes
as `Release014Vertical` and `Release014Landscape`: a hook, the version reveal,
a montage of the redesigned TUI, four chat beats (rewind, fork and compare, hunk review, shell passthrough),
a chip burst of the rest of the release and an update/repo-link card. It reuses
`src/tui/kit.tsx` and `FeatureTUI`; `ChatFrame` in `Chat.tsx` is the chat
workspace, modelled on `docs/assets/tui-chat*.png`.

```sh
npm run render:014   # writes out/vincent-0.14.0-vertical.mp4 and -landscape.mp4
npm run thumb:014    # writes out/vincent-0.14.0-thumbnail.png, the 1280×720 YouTube cover
```

Its soundtrack is `public/music-014.wav` (`npm run music:014`); update that
script's length, drop and outro when `Release014.tsx`'s timeline changes.
The redesign montage shows real captures, not a recreation:
`scripts/copy-shots.mjs` (run by `music:014`) copies them from `docs/assets/`
into `public/shots/`, so re-running `scripts/screenshots.sh` updates the video.

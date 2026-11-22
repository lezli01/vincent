import React from "react";
import { AbsoluteFill, Audio, Series, staticFile } from "remotion";
import { Backdrop, Progress } from "../components";
import { FeatureTUI } from "../scenes";
import { C } from "../theme";
import { ChatDiff } from "./ChatDiff";
import { Compare } from "./Compare";
import { Redesign, REDESIGN_FRAMES } from "./Redesign";
import { Rewind } from "./Rewind";
import { CTA014, Hook014, More014, Reveal014 } from "./scenes";
import { Shell } from "./Shell";

// The 0.14.0 release cut: the redesigned TUI as a montage of real captures,
// the four headline chat features as TUI beats, then
// the rest of the release as a chip burst. Music is scripts/make-music.mjs
// with this timeline's length, drop (the reveal) and outro (the CTA) — see
// package.json's music:014 script, and update it when this changes.
const timeline: [React.ReactNode, number][] = [
  [<Hook014 />, 60],
  [<Reveal014 />, 75],
  [<Redesign n={1} />, REDESIGN_FRAMES],
  [
    <FeatureTUI n={2} kicker="Rewind" headline="Undo a turn. *Code* *and* *conversation.*"
      sub="Every turn is checkpointed. alt+z puts the worktree back — and your prompt in the draft." Tui={Rewind} accent={C.teal} />,
    150,
  ],
  [
    <FeatureTUI n={3} kicker="Fork & compare" headline="Same prompt. *Two* *agents.* Keep the better one."
      sub="Fork from any turn onto claude, codex, cursor or copilot, side by side." Tui={Compare} accent={C.violet} />,
    150,
  ],
  [
    <FeatureTUI n={4} kicker="Hunk review" headline="Review the chat's diff, *hunk* *by* *hunk.*"
      sub="x reverts a hunk in the worktree and tells the agent not to bring it back." Tui={ChatDiff} accent={C.green} />,
    140,
  ],
  [
    <FeatureTUI n={5} kicker="Shell passthrough" headline="Run a command. *Show* *the* *agent.*"
      sub="alt+x runs in the chat's worktree; a attaches the output to your next message." Tui={Shell} accent={C.amber} />,
    135,
  ],
  [<More014 />, 105],
  [<CTA014 />, 110],
];

export const TOTAL_FRAMES_014 = timeline.reduce((n, [, d]) => n + d, 0);

export const Release014: React.FC = () => (
  <AbsoluteFill style={{ background: C.bg }}>
    <Backdrop />
    <Audio src={staticFile("music-014.wav")} volume={0.6} />
    <Series>
      {timeline.map(([node, d], i) => (
        <Series.Sequence key={i} durationInFrames={d}>
          {node}
        </Series.Sequence>
      ))}
    </Series>
    <Progress />
  </AbsoluteFill>
);

import React from "react";
import { AbsoluteFill, Easing, Img, interpolate, spring, staticFile, useCurrentFrame, useVideoConfig } from "remotion";
import { Headline, Kicker, SceneFade } from "../components";
import { C, mono, sans, useIsVertical } from "../theme";

// The TUI redesign (task 150, #1005; the task workspace, #1036), shown with
// the real captures scripts/screenshots.sh takes of it rather than redrawn:
// scripts/copy-shots.mjs copies them from docs/assets/ into public/shots/.

type Shot = { file: string; aspect: number; label: string; color: string };

const SHOTS: Shot[] = [
  { file: "tui-board", aspect: 3600 / 1250, label: "Task board · cards grouped by issue · live preview", color: C.teal },
  { file: "tui-issues-preview", aspect: 3600 / 1250, label: "Issues board · lanes of cards · details pane", color: C.blue },
  { file: "tui-task-overview-running", aspect: 2400 / 1250, label: "Task workspace · steps rail · Overview cards", color: C.violet },
  { file: "tui-task-output", aspect: 2400 / 1250, label: "Output · the whole task as one feed", color: C.green },
  { file: "tui-new-task", aspect: 3600 / 1250, label: "New task · one screen, not a six-stage wizard", color: C.amber },
  { file: "tui-project-overview", aspect: 3600 / 1250, label: "Projects · a card grid beside what needs you", color: C.teal },
  { file: "tui-daemon-adapters", aspect: 3600 / 1400, label: "Daemon · a dashboard with adapter cards", color: C.violet },
];

export const SHOT_START = 16;
export const SHOT_LEN = 30;
export const REDESIGN_FRAMES = SHOT_START + SHOTS.length * SHOT_LEN + 12;

const clamp = { extrapolateLeft: "clamp", extrapolateRight: "clamp" } as const;

const Slide: React.FC<{ shot: Shot; i: number; boxW: number; boxH: number; pan: boolean }> = ({ shot, i, boxW, boxH, pan }) => {
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  const t = frame - (SHOT_START + i * SHOT_LEN);
  const last = i === SHOTS.length - 1;
  if (t < 0 || (!last && t >= SHOT_LEN + 10)) return null;
  const enter = spring({ frame: t, fps, config: { damping: 15, mass: 0.7 } });
  const exit = last ? 0 : interpolate(t, [SHOT_LEN, SHOT_LEN + 10], [0, 1], clamp);
  // Landscape fits the capture into the stage; vertical fills the stage's
  // height and pans across it, so a 3:1 screen stays legible on a phone.
  let w = boxW;
  let h = boxW / shot.aspect;
  if (pan || h > boxH) {
    h = boxH;
    w = boxH * shot.aspect;
  }
  const travel = Math.max(0, w - boxW);
  const p = interpolate(t, [2, SHOT_LEN + 8], [0, 1], { ...clamp, easing: Easing.inOut(Easing.quad) });
  const panX = pan ? -travel * p : 0;
  const frameW = Math.min(w, boxW);
  return (
    <div
      style={{
        position: "absolute",
        inset: 0,
        opacity: enter * (1 - exit),
        transform: `translateX(${(1 - enter) * 260 - exit * 260}px) scale(${0.94 + enter * 0.06 - exit * 0.04}) perspective(1600px) rotateY(${(1 - enter) * -10 + exit * 10}deg)`,
      }}
    >
      <div style={{ position: "absolute", left: (boxW - frameW) / 2, top: (boxH - h) / 2, width: frameW, height: h, overflow: "hidden", borderRadius: 18, boxShadow: `0 40px 120px rgba(0,0,0,0.7), 0 0 0 2px #2a2e42, 0 0 80px ${shot.color}33` }}>
        <Img src={staticFile(`shots/${shot.file}.png`)} style={{ position: "absolute", left: panX, top: 0, width: w, height: h }} />
      </div>
    </div>
  );
};

export const Redesign: React.FC<{ n: number }> = ({ n }) => {
  const frame = useCurrentFrame();
  const vertical = useIsVertical();
  const subO = interpolate(frame, [14, 26], [0, 1], clamp);
  const boxW = vertical ? 1000 : 1720;
  const boxH = vertical ? 760 : 620;
  const k = Math.min(SHOTS.length - 1, Math.max(0, Math.floor((frame - SHOT_START) / SHOT_LEN)));
  const shot = SHOTS[k];
  const labelO = interpolate(frame - (SHOT_START + k * SHOT_LEN), [4, 10], [0, 1], clamp);
  return (
    <SceneFade>
      <AbsoluteFill
        style={{
          flexDirection: "column",
          alignItems: "center",
          justifyContent: vertical ? "center" : "flex-start",
          padding: vertical ? "0 40px 40px" : "56px 100px",
          gap: vertical ? 40 : 30,
        }}
      >
        <div style={{ display: "flex", flexDirection: "column", gap: 14, alignItems: vertical ? "center" : "flex-start", alignSelf: vertical ? "center" : "stretch" }}>
          <Kicker color={C.teal}>{String(n).padStart(2, "0")} — Redesigned TUI</Kicker>
          <Headline text="A whole *new* *TUI.* Every screen, rebuilt." accent={C.teal} align={vertical ? "center" : "left"} size={vertical ? 80 : 66} />
          <div style={{ fontFamily: sans, fontWeight: 500, fontSize: vertical ? 36 : 30, color: C.dim, opacity: subO, textAlign: vertical ? "center" : "left" }}>
            Boards of cards, a new task workspace, wide layouts — real captures.
          </div>
        </div>
        <div style={{ position: "relative", width: boxW, height: boxH }}>
          {SHOTS.map((s, i) => (
            <Slide key={s.file} shot={s} i={i} boxW={boxW} boxH={boxH} pan={vertical} />
          ))}
        </div>
        {frame >= SHOT_START && (
          <div style={{ display: "flex", alignItems: "center", gap: 18, opacity: labelO, fontFamily: mono, fontWeight: 700, fontSize: vertical ? 32 : 30, whiteSpace: "nowrap" }}>
            <span style={{ color: C.dim }}>
              {k + 1}/{SHOTS.length}
            </span>
            <span style={{ color: shot.color, padding: "8px 22px", borderRadius: 999, border: `3px solid ${shot.color}`, background: `${shot.color}1a` }}>{shot.label}</span>
          </div>
        )}
      </AbsoluteFill>
    </SceneFade>
  );
};

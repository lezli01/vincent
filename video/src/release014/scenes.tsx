import React from "react";
import { AbsoluteFill, Img, interpolate, spring, staticFile, useCurrentFrame, useVideoConfig } from "remotion";
import { Chip, Headline, Kicker, SceneFade } from "../components";
import { C, mono, sans, useIsVertical } from "../theme";
import { VERSION } from "./Chat";

const clamp = { extrapolateLeft: "clamp", extrapolateRight: "clamp" } as const;

// ─── Hook: the turn that went wrong ─────────────────────────────────────────

export const Hook014: React.FC = () => {
  const frame = useCurrentFrame();
  const vertical = useIsVertical();
  const flash = interpolate(frame, [50, 60], [0, 1], clamp);
  const lines: [string, string, number][] = [
    ["◆ claude · turn 3", C.violet, 14],
    ["✓ edit server.go  −212", C.text, 20],
    ["✓ bash rm -r internal/api/legacy", C.text, 26],
    ["✗ go test ./...   14 failed", C.red, 32],
  ];
  return (
    <AbsoluteFill style={{ alignItems: "center", justifyContent: "center", flexDirection: "column", gap: 50, padding: 60 }}>
      <Headline text="Your agent took a *wrong* *turn.*" accent={C.red} size={vertical ? 96 : 100} />
      <div style={{ fontFamily: mono, fontSize: vertical ? 34 : 36, background: "#0f121b", border: `2px solid ${C.border}`, borderRadius: 16, padding: "24px 36px", minWidth: vertical ? 800 : 820 }}>
        {lines.map(([t, c, at]) => (
          <div key={t} style={{ color: c, opacity: interpolate(frame, [at, at + 4], [0, 1], clamp), whiteSpace: "nowrap", transform: `translateX(${frame > 38 && c === C.red ? Math.sin(frame * 3) * 4 : 0}px)` }}>
            {t}
          </div>
        ))}
      </div>
      <AbsoluteFill style={{ background: "white", opacity: flash }} />
    </AbsoluteFill>
  );
};

// ─── Reveal: logo + version ─────────────────────────────────────────────────

export const Reveal014: React.FC = () => {
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  const vertical = useIsVertical();
  const flash = interpolate(frame, [0, 10], [1, 0], clamp);
  const s = spring({ frame, fps, config: { damping: 10, mass: 0.8 } });
  const v = spring({ frame: frame - 12, fps, config: { damping: 9, mass: 0.6 } });
  return (
    <AbsoluteFill style={{ justifyContent: "center", alignItems: "center", flexDirection: "column", gap: 24 }}>
      <Img
        src={staticFile("logo.png")}
        style={{
          width: vertical ? 900 : 900,
          transform: `scale(${0.6 + s * 0.4})`,
          opacity: s,
          filter: `drop-shadow(0 0 60px ${C.teal}66)`,
          marginBottom: vertical ? -110 : -130,
          marginTop: vertical ? -80 : -120,
        }}
      />
      <div
        style={{
          fontFamily: mono,
          fontWeight: 700,
          fontSize: vertical ? 110 : 120,
          color: C.teal,
          transform: `scale(${v})`,
          textShadow: `0 0 50px ${C.teal}aa`,
        }}
      >
        {VERSION}
      </div>
      <div style={{ padding: "0 60px" }}>
        <Headline text="A *new* *TUI.* Chats that rewind, fork and compare." accent={C.teal} delay={22} size={vertical ? 70 : 64} />
      </div>
      <AbsoluteFill style={{ background: "white", opacity: flash }} />
    </AbsoluteFill>
  );
};

// ─── Burst: everything else in the release ─────────────────────────────────

const MORE: [string, string][] = [
  ["queue messages mid-turn", C.teal],
  ["drafts kept by the daemon", C.violet],
  ["#issue-N · @file#L20-45 mentions", C.amber],
  ["search every chat", C.teal],
  ["cost per turn · ctx gauge", C.green],
  ["export md · html · json", C.blue],
  ["switch model between turns", C.violet],
  ["vim mode", C.green],
  ["Nerd Font icons", C.amber],
  ["chat notifications", C.amber],
  ["global pause", C.red],
  ["auto-archive finished tasks", C.teal],
  ["multi-project Jira", C.blue],
  ["vincent update --master", C.violet],
];

export const More014: React.FC = () => {
  const vertical = useIsVertical();
  return (
    <SceneFade>
      <AbsoluteFill style={{ alignItems: "center", justifyContent: "center", flexDirection: "column", gap: 44, padding: vertical ? 50 : 100 }}>
        <Kicker color={C.amber}>Also in {VERSION}</Kicker>
        <Headline text="And a *lot* *more.*" accent={C.amber} size={vertical ? 92 : 84} />
        <div style={{ display: "flex", flexWrap: "wrap", gap: 18, justifyContent: "center", maxWidth: vertical ? 1000 : 1650 }}>
          {MORE.map(([label, color], i) => (
            <Chip key={label} label={label} color={color} delay={12 + i * 4} big={false} />
          ))}
        </div>
      </AbsoluteFill>
    </SceneFade>
  );
};

// ─── CTA ────────────────────────────────────────────────────────────────────

export const CTA014: React.FC = () => {
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  const vertical = useIsVertical();
  const cmd = "vincent update";
  const shown = cmd.slice(0, Math.max(0, Math.floor((frame - 6) / 2)));
  const s = spring({ frame: frame - 34, fps, config: { damping: 12 } });
  const glow = 30 + Math.sin(frame / 6) * 12;
  return (
    <AbsoluteFill style={{ alignItems: "center", justifyContent: "center", flexDirection: "column", gap: 60, padding: 40 }}>
      <div style={{ fontFamily: mono, fontSize: vertical ? 64 : 72, color: C.text, background: "#0f121b", border: `2px solid ${C.border}`, borderRadius: 18, padding: "22px 44px" }}>
        <span style={{ color: C.green }}>$ </span>
        {shown}
        <span style={{ opacity: frame % 30 < 18 ? 1 : 0, color: C.teal }}>▋</span>
      </div>
      <div
        style={{
          fontFamily: sans,
          fontWeight: 900,
          fontSize: vertical ? 70 : 96,
          letterSpacing: -2,
          color: C.text,
          textAlign: "center",
          transform: `scale(${0.8 + s * 0.2})`,
          opacity: s,
          textShadow: `0 0 ${glow}px ${C.teal}88`,
        }}
      >
        github.com/<span style={{ color: C.teal }}>lezli01/vincent</span>
      </div>
    </AbsoluteFill>
  );
};

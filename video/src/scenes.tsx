import React from "react";
import {
  AbsoluteFill,
  Img,
  interpolate,
  random,
  spring,
  staticFile,
  useCurrentFrame,
  useVideoConfig,
} from "remotion";
import { Chip, Headline, Kicker, SceneFade } from "./components";
import { C, mono, sans, useIsVertical } from "./theme";

// ─── 1. Hook: terminal chaos ────────────────────────────────────────────────

const LOGS = [
  ["$ claude", "> refactoring auth…", "? Allow edit to db.go [y/n]"],
  ["$ codex exec", "running tests…", "✗ 3 failed"],
  ["$ cursor-agent", "> planning…", "waiting for input"],
  ["$ claude", "merge conflict in api.go", "? what now"],
  ["$ copilot", "> writing docs…", "context window full"],
  ["$ claude", "> fixing lint…", "usage limit reached"],
  ["$ codex exec", "> branch?", "which worktree??"],
  ["$ claude", "> 14m elapsed", "did it finish?"],
];

const Term: React.FC<{ i: number; vertical: boolean }> = ({ i, vertical }) => {
  const frame = useCurrentFrame();
  const { fps, width, height } = useVideoConfig();
  const appear = i * 5;
  const s = spring({ frame: frame - appear, fps, config: { damping: 12 } });
  const w = vertical ? 520 : 560;
  const x = random(`x${i}`) * (width - w - 40) + 20;
  const y = random(`y${i}`) * (height - (vertical ? 700 : 360)) + (vertical ? 380 : 40);
  const rot = (random(`r${i}`) - 0.5) * 10;
  const shake = frame > 45 ? Math.sin(frame * 2.7 + i) * 5 : 0;
  const lines = LOGS[i % LOGS.length];
  const shown = Math.floor(interpolate(frame - appear, [4, 30], [0, lines.length], { extrapolateLeft: "clamp", extrapolateRight: "clamp" }));
  return (
    <div
      style={{
        position: "absolute",
        left: x + shake,
        top: y,
        width: w,
        background: "#0f121b",
        border: `2px solid ${C.border}`,
        borderRadius: 14,
        boxShadow: "0 20px 60px rgba(0,0,0,0.7)",
        transform: `scale(${s}) rotate(${rot}deg)`,
        fontFamily: mono,
        fontSize: 26,
        overflow: "hidden",
      }}
    >
      <div style={{ display: "flex", gap: 8, padding: "10px 14px", background: "#1a1f2c" }}>
        {[C.red, C.amber, C.green].map((c) => (
          <div key={c} style={{ width: 14, height: 14, borderRadius: 7, background: c }} />
        ))}
        <span style={{ color: C.dim, fontSize: 20, marginLeft: 10 }}>agent {i + 1}</span>
      </div>
      <div style={{ padding: "12px 18px", minHeight: 130 }}>
        {lines.slice(0, shown).map((l, k) => (
          <div key={k} style={{ color: k === lines.length - 1 ? C.amber : C.text, whiteSpace: "nowrap" }}>
            {l}
          </div>
        ))}
      </div>
    </div>
  );
};

export const Hook: React.FC = () => {
  const vertical = useIsVertical();
  const frame = useCurrentFrame();
  const count = Math.min(8, Math.floor(frame / 5) + 1);
  const flash = frame > 80 ? interpolate(frame, [80, 90], [0, 1], { extrapolateRight: "clamp" }) : 0;
  return (
    <AbsoluteFill>
      {Array.from({ length: count }).map((_, i) => (
        <Term key={i} i={i} vertical={vertical} />
      ))}
      <AbsoluteFill
        style={{
          justifyContent: vertical ? "flex-start" : "center",
          alignItems: "center",
          padding: vertical ? "150px 60px" : 80,
        }}
      >
        <div
          style={{
            background: "rgba(7,9,15,0.82)",
            padding: "30px 44px",
            borderRadius: 28,
            backdropFilter: "blur(8px)",
            maxWidth: vertical ? 980 : 1400,
          }}
        >
          <Headline text="8 AI agents. 8 terminals. *Zero* idea what's going on." accent={C.red} delay={6} size={vertical ? 86 : 92} />
        </div>
      </AbsoluteFill>
      <AbsoluteFill style={{ background: "white", opacity: flash }} />
    </AbsoluteFill>
  );
};

// ─── 2. Logo reveal ─────────────────────────────────────────────────────────

export const Reveal: React.FC = () => {
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  const vertical = useIsVertical();
  const flash = interpolate(frame, [0, 10], [1, 0], { extrapolateRight: "clamp" });
  const s = spring({ frame, fps, config: { damping: 10, mass: 0.8 } });
  return (
    <AbsoluteFill style={{ justifyContent: "center", alignItems: "center", gap: 30, flexDirection: "column" }}>
      <Img
        src={staticFile("logo.png")}
        style={{
          width: vertical ? 1000 : 1150,
          transform: `scale(${0.6 + s * 0.4})`,
          opacity: s,
          filter: `drop-shadow(0 0 60px ${C.teal}66)`,
          marginBottom: vertical ? -120 : -170,
          marginTop: vertical ? -80 : -150,
        }}
      />
      <div style={{ padding: "0 60px" }}>
        <Headline text="The all-in-one *AI* *SDLC* tool." delay={14} size={vertical ? 80 : 78} />
      </div>
      <AbsoluteFill style={{ background: "white", opacity: flash }} />
    </AbsoluteFill>
  );
};

// ─── 4. "Close the terminal" — the daemon keeps going ───────────────────────

export const Daemon: React.FC<{ n: number }> = ({ n }) => {
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  const vertical = useIsVertical();
  const close = spring({ frame: frame - 28, fps, config: { damping: 14 } });
  const tasks = [
    ["#8", "move to the v2 storage driver", 50],
    ["#21", "harden the rate limiter", 66],
    ["#23", "ship the usage-limit banner", 82],
    ["#26", "document rate-limit headers", 98],
  ] as const;
  return (
    <SceneFade>
      <AbsoluteFill style={{ alignItems: "center", justifyContent: "center", flexDirection: "column", gap: 50, padding: 60 }}>
        <Kicker color={C.violet}>{String(n).padStart(2, "0")} — Daemon</Kicker>
        <Headline text="Close your laptop lid. The work *keeps* *going.*" accent={C.violet} size={vertical ? 84 : 76} />
        <div style={{ position: "relative", width: vertical ? 900 : 1100, height: 470 }}>
          {/* the client window that gets closed */}
          <div
            style={{
              position: "absolute",
              inset: 0,
              borderRadius: 20,
              background: "#0f121b",
              border: `2px solid ${C.border}`,
              transform: `scale(${1 - close * 0.25}) translateY(${close * 60}px)`,
              opacity: 1 - close,
              fontFamily: mono,
              fontSize: 34,
              color: C.text,
              padding: 40,
            }}
          >
            $ vincent tui
            <div style={{ color: C.dim, marginTop: 20 }}>^C</div>
          </div>
          {/* the daemon, still running */}
          <div
            style={{
              position: "absolute",
              inset: 0,
              opacity: close,
              fontFamily: mono,
              fontSize: vertical ? 32 : 34,
              display: "flex",
              flexDirection: "column",
              gap: 22,
              justifyContent: "center",
            }}
          >
            <div style={{ display: "flex", alignItems: "center", gap: 16, color: C.violet, fontWeight: 700 }}>
              <div
                style={{
                  width: 22,
                  height: 22,
                  borderRadius: 11,
                  background: C.green,
                  boxShadow: `0 0 ${16 + Math.sin(frame / 4) * 10}px ${C.green}`,
                }}
              />
              vincent daemon · running
            </div>
            {tasks.map(([id, title, at]) => {
              const done = frame > at;
              return (
                <div key={id} style={{ display: "flex", gap: 18, color: done ? C.text : C.dim, whiteSpace: "nowrap" }}>
                  <span style={{ color: done ? C.green : C.amber, width: 34 }}>{done ? "✓" : "●"}</span>
                  <span style={{ color: C.teal }}>{id}</span>
                  <span style={{ overflow: "hidden", textOverflow: "ellipsis" }}>{title}</span>
                </div>
              );
            })}
          </div>
        </div>
      </AbsoluteFill>
    </SceneFade>
  );
};

// ─── 5. Proof strip ─────────────────────────────────────────────────────────

export const Proof: React.FC = () => {
  const vertical = useIsVertical();
  const items: [string, string][] = [
    ["100% local", C.teal],
    ["No stored credentials", C.green],
    ["macOS · Linux · Windows", C.violet],
    ["Open source", C.amber],
  ];
  return (
    <SceneFade>
      <AbsoluteFill style={{ alignItems: "center", justifyContent: "center", flexDirection: "column", gap: 34, padding: 60 }}>
        <Headline text="Your repos. Your keys. *Your* *machine.*" size={vertical ? 86 : 80} />
        <div
          style={{
            display: "flex",
            flexDirection: vertical ? "column" : "row",
            flexWrap: "wrap",
            gap: 22,
            alignItems: "center",
            justifyContent: "center",
            marginTop: 20,
          }}
        >
          {items.map(([t, c], i) => (
            <Chip key={t} label={t} color={c} delay={14 + i * 6} big={vertical} />
          ))}
        </div>
      </AbsoluteFill>
    </SceneFade>
  );
};

// ─── 6. Call to action ──────────────────────────────────────────────────────

export const CTA: React.FC = () => {
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  const vertical = useIsVertical();
  const s = spring({ frame: frame - 4, fps, config: { damping: 12 } });
  const glow = 30 + Math.sin(frame / 6) * 12;
  return (
    <AbsoluteFill style={{ alignItems: "center", justifyContent: "center", padding: 40 }}>
      <div
        style={{
          fontFamily: sans,
          fontWeight: 900,
          fontSize: vertical ? 76 : 104,
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

// ─── 3b. Feature beats over the recreated, animated TUI ─────────────────────

export type TuiProps = { w: number; h: number; vertical: boolean };

export const FeatureTUI: React.FC<{
  n: number;
  kicker: string;
  headline: string;
  sub: string;
  accent?: string;
  Tui: React.FC<TuiProps>;
}> = ({ n, kicker, headline, sub, accent = C.teal, Tui }) => {
  const vertical = useIsVertical();
  const frame = useCurrentFrame();
  const { fps, durationInFrames } = useVideoConfig();
  const subO = interpolate(frame, [14, 26], [0, 1], { extrapolateLeft: "clamp", extrapolateRight: "clamp" });
  const enter = spring({ frame: frame - 4, fps, config: { damping: 16 } });
  const drift = interpolate(frame, [0, durationInFrames], [0.975, 1.0]);
  // Vertical renders a smaller terminal and scales it up, so the TUI text
  // stays legible on a phone instead of leaving half the window empty.
  const w = vertical ? 860 : 1720;
  const h = vertical ? 1000 : 680;
  const zoom = vertical ? 1000 / 860 : 1;
  return (
    <SceneFade>
      <AbsoluteFill
        style={{
          flexDirection: "column",
          alignItems: "center",
          justifyContent: vertical ? "center" : "flex-start",
          padding: vertical ? "0 40px 40px" : "56px 100px",
          gap: vertical ? 44 : 34,
        }}
      >
        <div style={{ display: "flex", flexDirection: "column", gap: 14, alignItems: vertical ? "center" : "flex-start", alignSelf: vertical ? "center" : "stretch" }}>
          <Kicker color={accent}>
            {String(n).padStart(2, "0")} — {kicker}
          </Kicker>
          <Headline text={headline} accent={accent} align={vertical ? "center" : "left"} size={vertical ? 80 : 66} />
          <div style={{ fontFamily: sans, fontWeight: 500, fontSize: vertical ? 36 : 30, color: C.dim, opacity: subO, textAlign: vertical ? "center" : "left" }}>
            {sub}
          </div>
        </div>
        <div
          style={{
            transform: `translateY(${(1 - enter) * 120}px) scale(${(0.92 + enter * 0.08) * drift}) perspective(1600px) rotateX(${(1 - enter) * 12}deg)`,
            opacity: enter,
            width: w * zoom,
            height: h * zoom,
          }}
        >
          <div style={{ transform: `scale(${zoom})`, transformOrigin: "0 0" }}>
            <Tui w={w} h={h} vertical={vertical} />
          </div>
        </div>
      </AbsoluteFill>
    </SceneFade>
  );
};

// ─── SDLC strip: issue to merge, one tool ───────────────────────────────────

const STAGES: [string, string, string][] = [
  ["◎", "Issue", C.blue],
  ["✎", "Plan", C.violet],
  ["⌨", "Code", C.violet],
  ["✓", "Test", C.green],
  ["⌕", "Review", C.amber],
  ["⇡", "PR", C.teal],
  ["⇄", "Merge", C.green],
];

export const SDLC: React.FC = () => {
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  const vertical = useIsVertical();
  return (
    <SceneFade>
      <AbsoluteFill style={{ alignItems: "center", justifyContent: "center", flexDirection: "column", gap: vertical ? 60 : 50, padding: 60 }}>
        <Headline text="From issue to merged PR. *One* *tool.*" size={vertical ? 82 : 76} />
        <div
          style={{
            display: "flex",
            flexDirection: vertical ? "column" : "row",
            alignItems: "center",
            gap: vertical ? 6 : 10,
          }}
        >
          {STAGES.map(([icon, label, color], i) => {
            const at = 14 + i * 8;
            const sp = spring({ frame: frame - at, fps, config: { damping: 12, mass: 0.6 } });
            const lit = frame >= at + 6;
            return (
              <React.Fragment key={label}>
                {i > 0 && (
                  <div style={{ color: lit ? color : C.border, fontSize: vertical ? 34 : 40, fontFamily: mono, opacity: sp }}>
                    {vertical ? "↓" : "→"}
                  </div>
                )}
                <div
                  style={{
                    transform: `scale(${0.6 + sp * 0.4})`,
                    opacity: sp,
                    display: "flex",
                    alignItems: "center",
                    gap: 14,
                    fontFamily: sans,
                    fontWeight: 800,
                    fontSize: vertical ? 42 : 38,
                    color: lit ? C.text : C.dim,
                    padding: vertical ? "10px 34px" : "14px 22px",
                    borderRadius: 18,
                    border: `3px solid ${lit ? color : C.border}`,
                    background: lit ? `${color}22` : "transparent",
                    boxShadow: lit ? `0 0 30px ${color}55` : undefined,
                    minWidth: vertical ? 340 : undefined,
                    justifyContent: "center",
                  }}
                >
                  <span style={{ color, fontFamily: mono }}>{icon}</span> {label}
                </div>
              </React.Fragment>
            );
          })}
        </div>
      </AbsoluteFill>
    </SceneFade>
  );
};

// ─── Engine capabilities burst ──────────────────────────────────────────────

const CAPS: [string, string][] = [
  ["agent", C.violet],
  ["command", C.green],
  ["manual", C.amber],
  ["parallel", C.teal],
  ["fan_out", C.teal],
  ["condition", C.blue],
  ["loop", C.blue],
  ["break", C.blue],
  ["include", C.violet],
  ["check", C.green],
  ["retries", C.amber],
  ["timeouts", C.amber],
  ["templates", C.teal],
  ["typed fields", C.violet],
  ["validation", C.green],
  ["hot reload", C.teal],
];

export const Capabilities: React.FC<{ n: number }> = ({ n }) => {
  const vertical = useIsVertical();
  return (
    <SceneFade>
      <AbsoluteFill style={{ alignItems: "center", justifyContent: "center", flexDirection: "column", gap: 44, padding: vertical ? 50 : 100 }}>
        <Kicker color={C.violet}>{String(n).padStart(2, "0")} — Workflow engine</Kicker>
        <Headline text="A real *workflow* *engine.* In plain YAML." accent={C.violet} size={vertical ? 80 : 74} />
        <div style={{ display: "flex", flexWrap: "wrap", gap: 18, justifyContent: "center", maxWidth: vertical ? 980 : 1600 }}>
          {CAPS.map(([label, color], i) => (
            <Chip key={label} label={label} color={color} delay={12 + i * 3} big={vertical} />
          ))}
        </div>
      </AbsoluteFill>
    </SceneFade>
  );
};

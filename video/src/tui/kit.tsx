import React from "react";
import { interpolate, spring, useCurrentFrame, useVideoConfig } from "remotion";
import { mono } from "../theme";

// The TUI paints with the 16 ANSI colours; scripts/screenshots.sh photographs
// it under VHS's TokyoNight theme, so these are that theme's values.
export const T = {
  bg: "#1a1b26",
  chrome: "#16161e",
  fg: "#c0caf5",
  dim: "#565f89",
  border: "#3b4261",
  sel: "#2a2e42", // colour 237, the selected-row background
  pill: "#414868", // colour 8
  cyan: "#7dcfff",
  blue: "#7aa2f7",
  green: "#4fd6be",
  yellow: "#e0af68",
  red: "#f7768e",
  magenta: "#bb9af7",
};

export const FONT = 20;
export const LH = 1.36;
export const ROW = FONT * LH;
export const CH = FONT * 0.6; // monospace advance

export const clamp = { extrapolateLeft: "clamp", extrapolateRight: "clamp" } as const;

/** How far (0..1) a spring has travelled since frame `at`. */
export const useSpringAt = (at: number, damping = 16) => {
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  return spring({ frame: frame - at, fps, config: { damping, mass: 0.7 } });
};

export type Key = { f: number; key: string; label: string };

/** The key most recently pressed, if it was pressed within `hold` frames. */
export const activeKey = (keys: Key[], frame: number, hold = 30) =>
  [...keys].reverse().find((k) => frame >= k.f && frame < k.f + hold);

/** Text typed out from frame `from` at `cps` characters per second. */
export const typed = (text: string, frame: number, from: number, cps = 28, fps = 30) =>
  text.slice(0, Math.max(0, Math.floor(((frame - from) / fps) * cps)));

export const Spinner: React.FC<{ color?: string }> = ({ color = T.cyan }) => {
  const frame = useCurrentFrame();
  const glyphs = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏";
  return <span style={{ color }}>{glyphs[Math.floor(frame / 2) % glyphs.length]}</span>;
};

/** A box-drawn panel with its title sitting on the top border, as lipgloss draws it. */
export const Panel: React.FC<{
  title?: React.ReactNode;
  right?: React.ReactNode;
  color?: string;
  style?: React.CSSProperties;
  children?: React.ReactNode;
  glow?: number;
}> = ({ title, right, color = T.border, style, children, glow = 0 }) => {
  // overflow belongs to the content, never the frame: the title sits on the
  // top border, half outside the box, and would be clipped with it.
  const { overflow, ...frameStyle } = style ?? {};
  return (
  <div
    style={{
      position: "relative",
      border: `2px solid ${color}`,
      borderRadius: 4,
      padding: `${ROW * 0.55}px ${CH}px ${ROW * 0.3}px`,
      boxShadow: glow ? `0 0 ${24 * glow}px ${color}` : undefined,
      ...frameStyle,
    }}
  >
    {title !== undefined && (
      <div
        style={{
          position: "absolute",
          top: -ROW / 2,
          left: CH,
          right: CH,
          display: "flex",
          justifyContent: "space-between",
          pointerEvents: "none",
        }}
      >
        <span style={{ background: T.bg, padding: `0 ${CH / 2}px`, fontWeight: 700 }}>{title}</span>
        {right !== undefined && <span style={{ background: T.bg, padding: `0 ${CH / 2}px` }}>{right}</span>}
      </div>
    )}
    <div style={{ overflow: overflow ?? "visible", height: "100%" }}>{children}</div>
  </div>
  );
};

export const Pill: React.FC<{ bg: string; fg?: string; children: React.ReactNode }> = ({ bg, fg = T.bg, children }) => (
  <span style={{ background: bg, color: fg, padding: `0 ${CH / 2}px`, fontWeight: 700 }}>{children}</span>
);

/** The footer's key hints. A hint whose key was just pressed lights up. */
export const Footer: React.FC<{ mode: string; hints: [string, string][]; keys: Key[] }> = ({ mode, hints, keys }) => {
  const frame = useCurrentFrame();
  const k = activeKey(keys, frame, 14);
  return (
    <div style={{ display: "flex", gap: CH * 1.2, whiteSpace: "nowrap", overflow: "hidden", alignItems: "center" }}>
      <Pill bg={T.pill} fg={T.fg}>
        ▶ {mode}
      </Pill>
      {hints.map(([key, label]) => {
        const hot = k?.key === key;
        return (
          <span
            key={key + label}
            style={{
              background: hot ? T.cyan : "transparent",
              borderRadius: 3,
              padding: `0 ${CH / 3}px`,
              color: hot ? T.bg : undefined,
            }}
          >
            <span style={{ color: hot ? T.bg : T.cyan, fontWeight: 700 }}>{key}</span>{" "}
            <span style={{ color: hot ? T.bg : T.dim }}>{label}</span>
          </span>
        );
      })}
    </div>
  );
};

/** The terminal window: chrome, the header line every view shares, a body and the footer. */
export const Term: React.FC<{
  w: number;
  h: number;
  crumb: React.ReactNode;
  footer: React.ReactNode;
  children: React.ReactNode;
}> = ({ w, h, crumb, footer, children }) => (
  <div
    style={{
      width: w,
      height: h,
      background: T.bg,
      color: T.fg,
      fontFamily: mono,
      fontSize: FONT,
      lineHeight: LH,
      borderRadius: 18,
      overflow: "hidden",
      display: "flex",
      flexDirection: "column",
      boxShadow: "0 40px 120px rgba(0,0,0,0.65), 0 0 0 2px #2a2e42, 0 0 90px rgba(125,207,255,0.12)",
    }}
  >
    <div style={{ display: "flex", gap: 9, padding: "14px 18px 4px", background: T.bg }}>
      {["#ff5f57", "#febc2e", "#28c840"].map((c) => (
        <div key={c} style={{ width: 13, height: 13, borderRadius: 7, background: c }} />
      ))}
    </div>
    <div
      style={{
        display: "flex",
        justifyContent: "space-between",
        padding: `${ROW * 0.3}px ${CH * 2}px`,
        borderBottom: `1px solid ${T.border}`,
        whiteSpace: "nowrap",
      }}
    >
      <span style={{ overflow: "hidden", minWidth: 0 }}>
        <span style={{ color: T.magenta, fontWeight: 700 }}>◆ api</span>{" "}
        <span style={{ color: T.yellow, fontWeight: 700 }}>(! 5 elsewhere)</span> {crumb}
      </span>
      {w > 1200 && <span style={{ color: T.dim, flexShrink: 0, marginLeft: CH }}>vincent 0.13.0</span>}
    </div>
    <div style={{ flex: 1, position: "relative", padding: `${ROW * 0.8}px ${CH * 1.5}px ${ROW * 0.4}px`, minHeight: 0 }}>
      {children}
    </div>
    <div style={{ padding: `${ROW * 0.25}px ${CH * 1.5}px ${ROW * 0.5}px` }}>{footer}</div>
  </div>
);

/** A screencast-style keycap that pops up for every key the "user" presses. */
export const KeyCast: React.FC<{ keys: Key[] }> = ({ keys }) => {
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  const k = activeKey(keys, frame, 34);
  if (!k) return null;
  const s = spring({ frame: frame - k.f, fps, config: { damping: 11, mass: 0.6 } });
  const out = interpolate(frame - k.f, [24, 34], [1, 0], clamp);
  const press = interpolate(frame - k.f, [0, 3, 7], [0, 6, 0], clamp);
  return (
    <div
      style={{
        position: "absolute",
        left: 0,
        right: 0,
        bottom: -48,
        display: "flex",
        justifyContent: "center",
        pointerEvents: "none",
        opacity: out,
        transform: `translateY(${(1 - s) * 50}px) scale(${0.7 + s * 0.3})`,
        zIndex: 10,
      }}
    >
      <div
        style={{
          display: "flex",
          alignItems: "center",
          gap: 22,
          background: "rgba(13,14,22,0.92)",
          border: "2px solid #3b4261",
          borderRadius: 22,
          padding: "14px 28px 14px 16px",
          boxShadow: "0 20px 60px rgba(0,0,0,0.6)",
          fontFamily: mono,
        }}
      >
        <div
          style={{
            minWidth: 72,
            height: 72,
            padding: "0 16px",
            borderRadius: 14,
            background: "linear-gradient(#f4f6fb, #c9cfdd)",
            color: "#1a1b26",
            fontSize: 38,
            fontWeight: 700,
            display: "flex",
            alignItems: "center",
            justifyContent: "center",
            boxShadow: `0 ${8 - press}px 0 #8a92a8, 0 ${10 - press}px 18px rgba(0,0,0,0.5)`,
            transform: `translateY(${press}px)`,
          }}
        >
          {k.key}
        </div>
        <div style={{ color: T.fg, fontSize: 34, fontWeight: 700, whiteSpace: "nowrap" }}>{k.label}</div>
      </div>
    </div>
  );
};

/**
 * Animates items between slots. `layouts` are the slot positions after each
 * event (index 0 is the initial layout); an item glides from its previous slot
 * to its new one with a spring starting at the event's frame.
 */
export function glide<P extends { x: number; y: number }>(
  frames: number[],
  layouts: Record<string, P>[],
  id: string,
  frame: number,
  fps: number,
): (P & { o: number }) | null {
  let k = 0;
  for (let i = 0; i < frames.length; i++) if (frame >= frames[i]) k = i + 1;
  const cur = layouts[k][id];
  const prev = k > 0 ? layouts[k - 1][id] : undefined;
  if (!cur) return null;
  if (!prev) {
    const s = k > 0 ? spring({ frame: frame - frames[k - 1], fps, config: { damping: 16 } }) : 1;
    return { ...cur, y: cur.y - (1 - s) * 30, o: s };
  }
  const s = spring({ frame: frame - frames[k - 1], fps, config: { damping: 15, mass: 0.8 } });
  return { ...cur, x: prev.x + (cur.x - prev.x) * s, y: prev.y + (cur.y - prev.y) * s, o: 1 };
}

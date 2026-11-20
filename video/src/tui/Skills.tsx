import React from "react";
import { useCurrentFrame, useVideoConfig } from "remotion";
import { mono } from "../theme";
import { CH, FONT, LH, ROW, Spinner, T, typed } from "./kit";

// A plain shell, not the vincent TUI: the skills are for agents you talk to
// directly, outside a vincent run.
export const Skills: React.FC<{ w: number; h: number; vertical: boolean }> = ({ w, h, vertical }) => {
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  const caret = Math.floor(frame / 8) % 2 === 0 ? "▋" : " ";
  const ask = "write a vincent workflow: lint, test, review, open a PR";
  const agents = "claude · codex · cursor";

  const lines: [number, React.ReactNode][] = [
    [0, <><span style={{ color: T.green }}>$ </span>{typed("vincent skills install", frame, 2, 30, fps)}{frame < 26 && <span style={{ color: T.cyan }}>{caret}</span>}</>],
    [28, <span style={{ color: T.dim }}>→ npx skills add lezli01/vincent --skill vincent-workflows …</span>],
    [36, <><span style={{ color: T.green }}>✓ vincent-workflows</span>  <span style={{ color: T.dim }}>installed · {agents}</span></>],
    [42, <><span style={{ color: T.green }}>✓ vincent-triggers </span>  <span style={{ color: T.dim }}>installed · {agents}</span></>],
    [50, <span> </span>],
    [52, <><span style={{ color: T.green }}>$ </span>claude</>],
    [56, <><span style={{ color: T.yellow, fontWeight: 700 }}>&gt; </span>{typed(ask, frame, 58, 46, fps)}{frame >= 58 && frame < 92 && <span style={{ color: T.cyan }}>{caret}</span>}</>],
    [96, <span style={{ color: T.magenta, fontWeight: 700 }}>◆ Skill(vincent-workflows)</span>],
    [102, <span style={{ color: T.blue }}>● Write .vincent/workflows/review-pr.yaml</span>],
    [108, <span style={{ color: T.blue }}>● Bash vincent workflow validate .vincent/workflows/review-pr.yaml</span>],
    [116, <span style={{ color: T.green }}>  ✓ valid · 5 steps · 2 agent, 2 command, 1 manual</span>],
    [122, <span>Done — agent steps only where judgement is needed.</span>],
  ];

  return (
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
        boxShadow: "0 40px 120px rgba(0,0,0,0.65), 0 0 0 2px #2a2e42, 0 0 90px rgba(187,154,247,0.15)",
      }}
    >
      <div style={{ display: "flex", gap: 9, padding: "14px 18px", alignItems: "center", borderBottom: `1px solid ${T.border}` }}>
        {["#ff5f57", "#febc2e", "#28c840"].map((c) => (
          <div key={c} style={{ width: 13, height: 13, borderRadius: 7, background: c }} />
        ))}
        <span style={{ color: T.dim, marginLeft: 12 }}>~/src/api — zsh</span>
      </div>
      <div style={{ padding: `${ROW * 0.6}px ${CH * 2}px`, whiteSpace: vertical ? "normal" : "nowrap" }}>
        {lines.map(([at, node], i) =>
          frame >= at ? (
            <div key={i} style={{ opacity: Math.min(1, (frame - at) / 4), marginBottom: vertical ? ROW * 0.2 : 0 }}>
              {node}
            </div>
          ) : null,
        )}
        {frame >= 92 && frame < 96 && (
          <div>
            <Spinner /> thinking…
          </div>
        )}
      </div>
    </div>
  );
};

import React from "react";
import { AbsoluteFill, Img, staticFile } from "remotion";
import { C, mono, sans } from "../theme";
import { VERSION } from "./Chat";

// The landscape upload's YouTube cover (1280×720): the release's two stories
// in four words — the new TUI and the chat verbs — over a real capture of the
// redesigned task board (public/shots, copied by scripts/copy-shots.mjs).
// The bottom-right corner stays empty: YouTube draws the duration there.

const VERBS: [string, string, string][] = [
  ["↺", "Rewind", C.teal],
  ["⑂", "Fork", C.violet],
  ["⇄", "Compare", C.amber],
];

export const Thumbnail: React.FC = () => (
  <AbsoluteFill style={{ background: C.bg, overflow: "hidden" }}>
    <AbsoluteFill
      style={{
        background: `radial-gradient(circle at 78% 40%, rgba(34,195,214,0.35), transparent 55%),
                     radial-gradient(circle at 20% 90%, rgba(180,140,255,0.25), transparent 50%), ${C.bg}`,
      }}
    />
    {/* the real redesigned board, tilted away to the right */}
    <div style={{ position: "absolute", left: 615, top: 140, perspective: 1600 }}>
      <Img
        src={staticFile("shots/tui-board.png")}
        style={{
          width: 980,
          borderRadius: 16,
          transform: "rotateY(-16deg) rotateX(4deg) rotateZ(-1.5deg)",
          transformOrigin: "left center",
          boxShadow: `0 40px 120px rgba(0,0,0,0.8), 0 0 0 2px #2a3142, 0 0 90px ${C.teal}66`,
        }}
      />
    </div>
    {/* and a real compare screen in front of it: two agents, side by side */}
    <div style={{ position: "absolute", left: 640, top: 470, perspective: 1600 }}>
      <Img
        src={staticFile("shots/tui-chat-compare.png")}
        style={{
          width: 540,
          borderRadius: 12,
          transform: "rotateY(-16deg) rotateX(4deg) rotateZ(-1.5deg)",
          transformOrigin: "left center",
          boxShadow: `0 30px 90px rgba(0,0,0,0.9), 0 0 0 2px ${C.amber}, 0 0 60px ${C.amber}66`,
        }}
      />
    </div>
    {/* a scrim so the type reads over the capture */}
    <AbsoluteFill style={{ background: `linear-gradient(90deg, ${C.bg} 0%, ${C.bg}ee 36%, ${C.bg}55 48%, transparent 58%)` }} />

    <div style={{ position: "absolute", left: 64, top: 40, display: "flex", flexDirection: "column", gap: 0 }}>
      <div style={{ display: "flex", alignItems: "center", gap: 20 }}>
        <Img src={staticFile("logo.png")} style={{ width: 300, marginLeft: -30, marginTop: -40, marginBottom: -50 }} />
        <div
          style={{
            fontFamily: mono,
            fontWeight: 700,
            fontSize: 40,
            color: C.bg,
            background: C.amber,
            padding: "4px 18px",
            borderRadius: 12,
            boxShadow: `0 0 40px ${C.amber}88`,
          }}
        >
          {VERSION}
        </div>
      </div>
      <div
        style={{
          fontFamily: sans,
          fontWeight: 900,
          fontSize: 128,
          lineHeight: 1,
          letterSpacing: -6,
          color: C.text,
          marginTop: 30,
          whiteSpace: "nowrap",
          textShadow: "0 8px 40px rgba(0,0,0,0.8)",
        }}
      >
        New <span style={{ color: C.teal, textShadow: `0 0 50px ${C.teal}aa` }}>TUI.</span>
      </div>
      <div style={{ display: "flex", flexDirection: "column", gap: 16, marginTop: 30 }}>
        {VERBS.map(([icon, word, color]) => (
          <div key={word} style={{ display: "flex", alignItems: "center", gap: 18, fontFamily: sans, fontWeight: 900, fontSize: 64, letterSpacing: -2, color }}>
            <span
              style={{
                width: 66,
                height: 66,
                borderRadius: 16,
                border: `4px solid ${color}`,
                background: `${color}22`,
                display: "flex",
                alignItems: "center",
                justifyContent: "center",
                fontSize: 36,
                fontFamily: mono,
              }}
            >
              {icon}
            </span>
            <span style={{ textShadow: `0 0 30px ${color}88` }}>{word}</span>
          </div>
        ))}
      </div>
    </div>
  </AbsoluteFill>
);

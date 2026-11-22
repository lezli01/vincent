import React from "react";
import { interpolate, useCurrentFrame } from "remotion";
import { CH, clamp, Key, ROW, T } from "../tui/kit";
import { ChatFrame, Tool, TurnHead, You } from "./Chat";

// Task 169: alt+d swaps the conversation for the chat's diff, `]`/`[` walk
// the hunks, `x` reverts one in the working tree and leaves a note at the top
// of the draft, `a` marks one accepted.

export const DF_KEYS: Key[] = [
  { f: 16, key: "alt+d", label: "chat diff" },
  { f: 42, key: "]", label: "next hunk" },
  { f: 64, key: "x", label: "reject hunk" },
  { f: 92, key: "[", label: "previous hunk" },
  { f: 108, key: "a", label: "accept hunk" },
];

type Hunk = { head: string; lines: [string, string][] };

const HUNKS: Hunk[] = [
  {
    head: "@@ -41,7 +41,9 @@ func (s *Server) routes()",
    lines: [
      ["-", '    mux.Handle("/v1/", legacy.Wrap(api))'],
      ["+", "    lim := newLimiter(cfg.Routes)"],
      ["+", '    mux.Handle("/v1/", lim.wrap(api))'],
    ],
  },
  {
    head: "@@ -88,9 +90,6 @@ func (s *Server) upload()",
    lines: [
      ["-", "    if !authorized(r) {"],
      ["-", "        return errForbidden"],
      ["-", "    }"],
    ],
  },
];

export const ChatDiff: React.FC<{ w: number; h: number; vertical: boolean }> = ({ w, h, vertical }) => {
  const frame = useCurrentFrame();
  const [kDiff, kNext, kReject, kPrev, kAccept] = DF_KEYS.map((k) => k.f);
  const diff = frame >= kDiff;
  const cur = frame >= kNext && frame < kPrev ? 1 : 0;
  const rejected = frame >= kReject;
  const accepted = frame >= kAccept;
  const rejO = interpolate(frame, [kReject, kReject + 8], [0, 1], clamp);
  const flash = (at: number) => interpolate(frame, [at, at + 4, at + 16], [0, 1, 0], clamp);

  const note = rejected ? (
    <span style={{ opacity: rejO }}>
      <span style={{ color: T.yellow }}>The user reverted these hunks; do not reapply them:</span>
      {"\n"}
      <span style={{ color: T.dim }}>- internal/api/server.go @@ -88,9 +90,6 @@ func (s *Server) upload()</span>
      {"\n"}
      <span style={{ background: T.fg }}> </span>
    </span>
  ) : undefined;

  return (
    <ChatFrame
      w={w}
      h={h}
      vertical={vertical}
      keys={DF_KEYS}
      ctx={44}
      sub={diff ? <span>diff · since base · <span style={{ color: T.green }}>+2</span> <span style={{ color: T.red }}>−4</span> · untracked included</span> : undefined}
      draft={note}
      hints={
        diff
          ? vertical
            ? [["]/[", "hunks"], ["x", "reject"], ["a", "accept"], ["esc", "back"]]
            : [["]/[", "hunks"], ["x", "reject"], ["a", "accept"], ["s", "since last turn"], ["esc", "back"]]
          : undefined
      }
    >
      {!diff ? (
        <>
          <You>finish the per-route limiter</You>
          <TurnHead n={4} stats="◷ now · 52s · 20.1k↑ 1.4k↓ · $0.05" />
          <Tool>edit internal/api/server.go <span style={{ color: T.green }}>+2</span> <span style={{ color: T.red }}>−4</span></Tool>
          <div>Done — the limiter wraps every route.</div>
        </>
      ) : (
        <div style={{ whiteSpace: "nowrap", overflow: "hidden", alignSelf: "stretch", flex: 1 }}>
          <div style={{ fontWeight: 700, marginBottom: ROW * 0.3 }}>
            ▾ internal/api/server.go <span style={{ color: T.green }}>+2</span> <span style={{ color: T.red }}>−4</span>
          </div>
          {HUNKS.map((hk, i) => {
            const on = cur === i;
            const isRej = i === 1 && rejected;
            const isAcc = i === 0 && accepted;
            return (
              <div
                key={i}
                style={{
                  borderLeft: `4px solid ${on ? T.cyan : "transparent"}`,
                  paddingLeft: CH,
                  marginBottom: ROW * 0.5,
                  background: on ? T.sel : undefined,
                  boxShadow: i === 1 ? `inset 0 0 0 999px rgba(247,118,142,${0.25 * flash(kReject)})` : i === 0 ? `inset 0 0 0 999px rgba(79,214,190,${0.25 * flash(kAccept)})` : undefined,
                }}
              >
                <div style={{ display: "flex", justifyContent: "space-between", flexWrap: "wrap", columnGap: CH * 2 }}>
                  <span style={{ color: T.cyan }}>{hk.head}</span>
                  {isRej && <span style={{ color: T.red, fontWeight: 700, opacity: rejO }}>✗ rejected · reverted</span>}
                  {isAcc && <span style={{ color: T.green, fontWeight: 700 }}>✓ accepted</span>}
                </div>
                {hk.lines.map(([sign, text], j) => (
                  <div key={j} style={{ whiteSpace: "pre", color: sign === "+" ? T.green : T.red, textDecoration: isRej ? "line-through" : undefined, opacity: isRej ? 0.5 : 1 }}>
                    {sign}
                    {text}
                  </div>
                ))}
              </div>
            );
          })}
        </div>
      )}
    </ChatFrame>
  );
};

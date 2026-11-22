import React from "react";
import { interpolate, useCurrentFrame } from "remotion";
import { clamp, Key, Pill, ROW, Spinner, T, typed } from "../tui/kit";
import { ChatFrame, Modal, Row, Tool, TurnHead, You } from "./Chat";

// Task 167: alt+z → pick the turn → pick what to put back → the code and the
// conversation return to before that turn, and its prompt returns to the draft.

export const RW_KEYS: Key[] = [
  { f: 40, key: "alt+z", label: "rewind" },
  { f: 62, key: "enter", label: "before turn 3" },
  { f: 84, key: "enter", label: "code + conversation" },
];

const PROMPT3 = "now clean up the old middleware";

export const Rewind: React.FC<{ w: number; h: number; vertical: boolean }> = ({ w, h, vertical }) => {
  const frame = useCurrentFrame();
  const [kOpen, kTurn, kDo] = RW_KEYS.map((k) => k.f);
  const picker = frame >= kOpen && frame < kDo;
  const modeMenu = frame >= kTurn;
  const done = frame >= kDo;
  const gone = interpolate(frame, [kDo, kDo + 12], [1, 0], clamp);
  const pickerO = picker ? interpolate(frame, [kOpen, kOpen + 5], [0, 1], clamp) : 0;
  const ctx = interpolate(frame, [kDo, kDo + 14], [86, 44], clamp);
  const t3 = [
    { f: 4, node: <Tool>edit internal/api/server.go <span style={{ color: T.red }}>−212</span></Tool> },
    { f: 12, node: <Tool>bash rm -r internal/api/legacy</Tool> },
    { f: 20, node: <Tool ok={false}>bash go test ./... <span style={{ color: T.red }}>14 failed</span></Tool> },
    { f: 28, node: <div style={{ color: T.red }}>The auth middleware lived in legacy/. Restoring it by hand…</div> },
  ];

  return (
    <ChatFrame
      w={w}
      h={h}
      vertical={vertical}
      keys={RW_KEYS}
      ctx={ctx}
      state={done ? <Pill bg={T.pill} fg={T.fg}>idle</Pill> : <Pill bg={T.red}>failed</Pill>}
      draft={
        done ? (
          <span>
            {typed(PROMPT3, frame, kDo + 10, 90)}
            <span style={{ background: T.fg }}> </span>
          </span>
        ) : undefined
      }
      overlay={
        picker && (
          <Modal title={<span style={{ color: T.cyan }}>{modeMenu ? "rewind to before turn 3" : "rewind to before turn…"}</span>} o={pickerO}>
            {!modeMenu ? (
              <>
                <Row on>turn 3 · {PROMPT3}</Row>
                <Row>turn 2 · make the limiter per-route</Row>
                <Row>turn 1 · where is the rate limit applied?</Row>
              </>
            ) : (
              <>
                <Row on>code + conversation</Row>
                <Row>code only</Row>
                <Row>conversation only</Row>
                <div style={{ color: T.dim, marginTop: ROW * 0.3 }}>the state it replaces is saved as rewound-1</div>
              </>
            )}
          </Modal>
        )
      }
    >
      {vertical ? (
        <>
          <You>where is the rate limit applied?</You>
          <TurnHead n={1} stats="◷ 9m · 22s · 12.0k↑ 0.8k↓ · $0.02" />
          <Tool>read internal/api/server.go</Tool>
          <div style={{ marginBottom: ROW * 0.6 }}>One global bucket, in server.go.</div>
        </>
      ) : (
        <div style={{ color: T.dim }}>⋯ turn 1</div>
      )}
      <You>make the limiter per-route</You>
      <TurnHead n={2} stats="◷ 4m · 31s · 18.2k↑ 1.1k↓ · $0.04" />
      <Tool>edit internal/api/ratelimit.go <span style={{ color: T.green }}>+38</span></Tool>
      <div style={{ marginBottom: ROW * 0.6 }}>Each route now gets its own bucket.</div>
      <div style={{ opacity: gone, maxHeight: ROW * 9 * gone, overflow: "hidden" }}>
        <You>{PROMPT3}</You>
        <TurnHead n={3} stats="◷ 1m · 48s · 22.4k↑ 1.9k↓ · $0.07" />
        {t3.filter((l) => frame >= l.f).map((l, i) => (
          <div key={i} style={{ opacity: Math.min(1, (frame - l.f) / 4) }}>
            {l.node}
          </div>
        ))}
        {frame < 28 && (
          <div>
            <Spinner /> <span style={{ color: T.dim }}>claude is working…</span>
          </div>
        )}
      </div>
      {done && (
        <div style={{ color: T.cyan, fontWeight: 700, opacity: interpolate(frame, [kDo + 6, kDo + 14], [0, 1], clamp), whiteSpace: "nowrap", overflow: "hidden", textOverflow: "ellipsis" }}>
          ↺ rewound to before turn 3 · code restored · 1 turn hidden
        </div>
      )}
    </ChatFrame>
  );
};

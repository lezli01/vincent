import React from "react";
import { interpolate, useCurrentFrame } from "remotion";
import { CH, clamp, Key, Panel, Pill, ROW, Spinner, T, typed } from "../tui/kit";
import { ChatFrame, Modal, TurnHead, You } from "./Chat";

// Task 170: alt+x runs one command in the chat's directory and draws it as a
// card with its exit code; `a` on the card hands it to the agent with the
// next message.

const CMD = "go test ./internal/api/...";
const MSG = "fix the failing test";

export const SH_KEYS: Key[] = [
  { f: 12, key: "alt+x", label: "shell" },
  { f: 44, key: "enter", label: "run" },
  { f: 70, key: "a", label: "attach to next message" },
  { f: 104, key: "enter", label: "send" },
];

export const Shell: React.FC<{ w: number; h: number; vertical: boolean }> = ({ w, h, vertical }) => {
  const frame = useCurrentFrame();
  const [kOpen, kRun, kAttach, kSend] = SH_KEYS.map((k) => k.f);
  const prompt = frame >= kOpen && frame < kRun;
  const card = frame >= kRun;
  const finished = frame >= kRun + 12;
  const attached = frame >= kAttach;
  const sent = frame >= kSend;
  const o = (at: number) => interpolate(frame, [at, at + 6], [0, 1], clamp);

  return (
    <ChatFrame
      w={w}
      h={h}
      vertical={vertical}
      keys={SH_KEYS}
      ctx={52}
      state={sent ? <Pill bg={T.cyan}>running</Pill> : undefined}
      draft={
        attached && !sent ? (
          <span>
            {typed(MSG, frame, kAttach + 8, 40)}
            <span style={{ background: T.fg }}> </span>
          </span>
        ) : undefined
      }
      overlay={
        prompt && (
          <Modal title={<span style={{ color: T.cyan }}>shell · runs in the chat's worktree</span>} o={o(kOpen)} width="62%">
            <span style={{ color: T.green }}>$ </span>
            {typed(CMD, frame, kOpen + 4, 40)}
            <span style={{ background: T.fg }}> </span>
          </Modal>
        )
      }
    >
      <TurnHead n={4} stats="◷ 2m · 52s · 20.1k↑ 1.4k↓ · $0.05" />
      <div style={{ marginBottom: ROW * 0.6 }}>Done — the limiter wraps every route.</div>
      {card && (
        <div style={{ opacity: o(kRun), marginBottom: ROW * 0.6 }}>
          <Panel
            color={attached ? T.cyan : T.border}
            title={
              <span style={{ whiteSpace: "nowrap" }}>
                <span style={{ color: T.green }}>$</span> {CMD}
              </span>
            }
            right={
              finished ? (
                <span style={{ color: T.red, fontWeight: 700 }}>{attached ? "↗ attached · " : ""}exit 1 · 2.1s</span>
              ) : (
                <Spinner />
              )
            }
          >
            {finished ? (
              <div style={{ whiteSpace: "nowrap", overflow: "hidden", textOverflow: "ellipsis" }}>
                <div style={{ color: T.red }}>--- FAIL: TestLimiterBurst (0.00s)</div>
                <div style={{ color: T.dim, paddingLeft: CH * 2 }}>ratelimit_test.go:42: got 429 after 19 requests, want 20</div>
                <div style={{ color: T.red }}>FAIL internal/api 0.38s</div>
              </div>
            ) : (
              <div style={{ color: T.dim }}>running…</div>
            )}
          </Panel>
        </div>
      )}
      {sent && (
        <div style={{ opacity: o(kSend) }}>
          <You tag="+ shell output">{MSG}</You>
          <TurnHead n={5} />
          <div>
            <Spinner /> <span style={{ color: T.dim }}>claude is reading the test output…</span>
          </div>
        </div>
      )}
    </ChatFrame>
  );
};

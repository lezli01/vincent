import React from "react";
import { interpolate, useCurrentFrame } from "remotion";
import { CH, clamp, Footer, Key, KeyCast, Panel, Pill, ROW, Spinner, T, Term } from "../tui/kit";
import { Tabs, Tool, TurnHead, VERSION } from "./Chat";

// Task 171, modelled on docs/assets/tui-chat-compare.png: one chat forked from
// before a turn onto two agents, both sent the same prompt; `]` keeps the
// right side and the left is archived with its branch.

export const CMP_KEYS: Key[] = [{ f: 112, key: "]", label: "keep right" }];

const PROMPT = "now clean up the old middleware — keep auth working";

type L = { f: number; node: React.ReactNode };

const LEFT: L[] = [
  { f: 14, node: <Tool>read internal/api/legacy/auth.go</Tool> },
  { f: 26, node: <Tool>edit internal/api/server.go <span style={{ color: T.green }}>+4</span> <span style={{ color: T.red }}>−188</span></Tool> },
  { f: 40, node: <Tool>bash go test ./...</Tool> },
  { f: 56, node: <div>Moved auth into the router, removed legacy/.</div> },
  { f: 64, node: <div style={{ color: T.yellow }}>2 handlers now skip the auth check.</div> },
];

const RIGHT: L[] = [
  { f: 18, node: <Tool>command_execution rg "legacy\." -l</Tool> },
  { f: 32, node: <Tool>edit internal/api/server.go <span style={{ color: T.green }}>+9</span> <span style={{ color: T.red }}>−171</span></Tool> },
  { f: 48, node: <Tool>command_execution go test ./...</Tool> },
  { f: 74, node: <div>Legacy removed; auth wraps every route.</div> },
  { f: 82, node: <div style={{ color: T.green }}>ok · 0 handlers unauthenticated.</div> },
];

const Side: React.FC<{ id: string; side: string; agent: string; lines: L[]; doneAt: number; kept?: boolean; dropped?: boolean; mode: string }> = ({
  id,
  side,
  agent,
  lines,
  doneAt,
  kept,
  dropped,
  mode,
}) => {
  const frame = useCurrentFrame();
  const running = frame < doneAt;
  const glow = kept ? interpolate(frame, [CMP_KEYS[0].f, CMP_KEYS[0].f + 10], [0, 1], clamp) : 0;
  return (
    <Panel
      color={kept ? T.green : T.border}
      glow={glow}
      title={
        <span>
          <span style={{ color: T.cyan }}>{id}</span> {side}
        </span>
      }
      right={
        dropped ? <Pill bg={T.pill} fg={T.fg}>archived</Pill> : kept ? <Pill bg={T.green}>kept</Pill> : running ? <Pill bg={T.cyan}>running</Pill> : <Pill bg={T.pill} fg={T.fg}>idle</Pill>
      }
      style={{ flex: 1, minWidth: 0, minHeight: 0, overflow: "hidden", opacity: dropped ? 0.45 : 1 }}
    >
      <div style={{ display: "flex", flexDirection: "column", height: "100%" }}>
        <TurnHead agent={agent} n={1} stats={running ? "◷ now" : `◷ now · ${(doneAt / 30).toFixed(1)}s`} />
        <div style={{ flex: 1, minHeight: 0, overflow: "hidden" }}>
          {lines.filter((l) => frame >= l.f).map((l, i) => (
            <div key={i} style={{ opacity: Math.min(1, (frame - l.f) / 4) }}>
              {l.node}
            </div>
          ))}
          {running && (
            <div>
              <Spinner /> <span style={{ color: T.dim }}>{agent} is working…</span>
            </div>
          )}
        </div>
        <div style={{ whiteSpace: "nowrap" }}>
          <span style={{ color: T.magenta }}>◆ {agent}</span> <span style={{ color: T.dim }}>·</span>{" "}
          <span style={{ color: mode === "restricted" ? T.green : T.yellow }}>{mode}</span>
        </div>
        <div style={{ color: T.dim, whiteSpace: "nowrap" }}>{side === "left" ? "[" : "]"} keep this side · enter opens it</div>
      </div>
    </Panel>
  );
};

export const Compare: React.FC<{ w: number; h: number; vertical: boolean }> = ({ w, h, vertical }) => {
  const frame = useCurrentFrame();
  const kept = frame >= CMP_KEYS[0].f;
  return (
    <div style={{ position: "relative" }}>
      <Term
        w={w}
        h={h}
        version={VERSION}
        crumb={<Tabs chats={6} />}
        footer={
          <Footer
            mode="compare"
            keys={CMP_KEYS}
            hints={vertical ? [["tab", "side"], ["[", "keep left"], ["]", "keep right"]] : [["tab", "side"], ["enter", "open"], ["[", "keep left"], ["]", "keep right"], ["R", "reload"]]}
          />
        }
      >
        <div style={{ display: "flex", flexDirection: "column", height: "100%", gap: ROW * 0.6 }}>
          <div style={{ whiteSpace: "nowrap", overflow: "hidden", textOverflow: "ellipsis" }}>
            <b>compare #1</b> <span style={{ color: T.dim }}>chat #7, from before turn 3</span>
            <br />
            <span style={{ color: T.dim }}>prompt:</span> {PROMPT}
          </div>
          <div style={{ display: "flex", flexDirection: vertical ? "column" : "row", gap: vertical ? ROW * 0.8 : CH * 1.5, flex: 1, minHeight: 0 }}>
            <Side id="#12" side="left" agent="claude" lines={LEFT} doneAt={70} dropped={kept} mode="restricted" />
            <Side id="#13" side="right" agent="codex" lines={RIGHT} doneAt={90} kept={kept} mode="full-auto" />
          </div>
        </div>
      </Term>
      <KeyCast keys={CMP_KEYS} />
    </div>
  );
};

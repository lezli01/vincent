import React from "react";
import { useCurrentFrame, useVideoConfig } from "remotion";
import { CH, Footer, glide, Key, KeyCast, Panel, Pill, ROW, Spinner, T, Term } from "./kit";

type St = "need" | "run" | "queue" | "done";
type Card = { id: string; wf: string; title: string; st: St; step: number; steps: string[] };
type State = { cols: Record<St, string[]>; cards: Record<string, Card>; sel: string };

const COLS: { k: St; title: string; icon: string; color: string }[] = [
  { k: "need", title: "Needs you", icon: "!", color: T.yellow },
  { k: "run", title: "Running", icon: "▶", color: T.cyan },
  { k: "queue", title: "Queued", icon: "◷", color: T.blue },
  { k: "done", title: "Finished", icon: "✓", color: T.green },
];

const initial: State = {
  sel: "8",
  cols: { need: [], run: ["8", "21", "1"], queue: ["23", "26"], done: ["2"] },
  cards: {
    "1": { id: "1", wf: "feature-pr", title: "add rate limiting to the public API", st: "run", step: 2, steps: ["implement", "commit", "review", "publish"] },
    "8": { id: "8", wf: "platform-upgrade", title: "move to the v2 storage driver", st: "run", step: 1, steps: ["plan", "upgrade", "merge"] },
    "21": { id: "21", wf: "verify-build", title: "harden the rate limiter", st: "run", step: 1, steps: ["build", "soak"] },
    "23": { id: "23", wf: "feature-delivery", title: "ship the usage-limit banner", st: "queue", step: 0, steps: ["plan", "deliver"] },
    "26": { id: "26", wf: "docs-refresh", title: "document the rate-limit headers", st: "queue", step: 0, steps: ["draft", "publish"] },
    "2": { id: "2", wf: "feature-pr", title: "bump the design tokens", st: "done", step: 4, steps: ["implement", "commit", "review", "publish"] },
  },
};

const move = (id: string, to: St, patch: Partial<Card> = {}) => (s: State): State => {
  const cols = Object.fromEntries(
    Object.entries(s.cols).map(([k, v]) => [k, v.filter((x) => x !== id)]),
  ) as State["cols"];
  cols[to] = to === "need" || to === "run" ? [id, ...cols[to]] : [...cols[to], id];
  return { ...s, cols, cards: { ...s.cards, [id]: { ...s.cards[id], st: to, ...patch } } };
};
const patch = (id: string, p: Partial<Card>) => (s: State): State => ({ ...s, cards: { ...s.cards, [id]: { ...s.cards[id], ...p } } });
const select = (id: string) => (s: State): State => ({ ...s, sel: id });

const EVENTS: { f: number; fn: (s: State) => State }[] = [
  { f: 20, fn: move("23", "run", { step: 0 }) },
  { f: 34, fn: move("1", "need", { step: 2 }) },
  { f: 56, fn: select("1") },
  { f: 82, fn: move("1", "run", { step: 3 }) },
  { f: 100, fn: move("21", "done", { step: 2 }) },
  { f: 112, fn: patch("8", { step: 2 }) },
  { f: 124, fn: move("1", "done", { step: 4 }) },
];

export const BOARD_KEYS: Key[] = [
  { f: 56, key: "H", label: "jump to what needs you" },
  { f: 82, key: "a", label: "approve" },
];

const STATES: State[] = EVENTS.reduce<State[]>((acc, e) => [...acc, e.fn(acc[acc.length - 1])], [initial]);

const Progress: React.FC<{ c: Card }> = ({ c }) => (
  <span>
    {c.steps.map((_, i) => (
      <span key={i} style={{ color: i < c.step ? T.green : i === c.step && c.st !== "queue" ? T.cyan : T.dim }}>
        {i < c.step ? "✓" : i === c.step && c.st !== "queue" ? "●" : "○"}
      </span>
    ))}
    <span style={{ color: T.dim }}>
      {" "}
      {Math.min(c.step + 1, c.steps.length)}/{c.steps.length} {c.steps[Math.min(c.step, c.steps.length - 1)]}
    </span>
  </span>
);

const Status: React.FC<{ c: Card; frame: number }> = ({ c, frame }) => {
  if (c.st === "need") return <span style={{ color: T.yellow, fontWeight: 700 }}>! needs your approval</span>;
  if (c.st === "queue") return <span style={{ color: T.dim }}>◷ waiting for a slot</span>;
  if (c.st === "done") return <span style={{ color: T.green }}>✓ done</span>;
  return (
    <span style={{ color: T.cyan }}>
      <Spinner /> running · {14 + Math.floor(frame / 30) + Number(c.id) % 9}s
    </span>
  );
};

export const Board: React.FC<{ w: number; h: number; vertical: boolean }> = ({ w, h, vertical }) => {
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  let k = 0;
  for (let i = 0; i < EVENTS.length; i++) if (frame >= EVENTS[i].f) k = i + 1;
  const state = STATES[k];

  const bodyW = w - CH * 3;
  const detailW = vertical ? 0 : bodyW * 0.25;
  const gap = CH;
  const colW = (bodyW - detailW - gap * (vertical ? 3 : 4)) / 4;
  const top = ROW * 2.2;
  const cardH = ROW * (vertical ? 5.6 : 4.6);
  const cardGap = ROW * 0.5;

  const layouts = STATES.map((s) => {
    const out: Record<string, { x: number; y: number }> = {};
    COLS.forEach((col, ci) =>
      s.cols[col.k].forEach((id, i) => {
        out[id] = { x: ci * (colW + gap) + CH * 0.6, y: top + ROW * 0.9 + i * (cardH + cardGap) };
      }),
    );
    return out;
  });

  const need = state.cols.need.length;
  const running = state.cols.run.length;
  const sel = state.cards[state.sel];

  return (
    <div style={{ position: "relative" }}>
      <Term
        w={w}
        h={h}
        crumb={
          <span style={{ color: T.dim }}>
            {" "}■ Issues (3) <Pill bg={T.pill} fg={T.fg}>▶ Tasks (6)</Pill> ○ Chats (3)
          </span>
        }
        footer={
          <Footer
            mode="Tasks"
            keys={BOARD_KEYS}
            hints={
              vertical
                ? [["H", "needs you"], ["a", "approve"], ["x", "reject"], ["?", "help"]]
                : [["enter", "open"], ["/", "filter"], ["H", "needs you"], ["a", "approve"], ["x", "reject"], ["s", "skip"], ["T", "chat"], ["?", "help"]]
            }
          />
        }
      >
        <Panel
          style={{ position: "absolute", left: CH * 1.5, right: CH * 1.5, top: 0, height: ROW * 1.7, padding: `${ROW * 0.25}px ${CH}px` }}
        >
          <div style={{ display: "flex", justifyContent: "space-between", whiteSpace: "nowrap" }}>
            <span style={{ color: T.dim }}>
              agents <span style={{ color: T.green }}>✓</span>{!vertical && <> <span style={{ color: T.yellow, fontWeight: 700 }}>claude</span> codex cursor</>}
            </span>
            <span style={{ color: T.dim }}>
              {need > 0 && <span style={{ color: T.yellow, fontWeight: 700 }}>! {need} need attention · </span>}
              {running} running{!vertical && " · cap 4"}
            </span>
          </div>
        </Panel>

        <div style={{ position: "absolute", left: CH * 1.5, top: 0, right: CH * 1.5, bottom: 0 }}>
          {COLS.map((col, ci) => {
            const n = state.cols[col.k].length;
            const flash = col.k === "need" && n > 0 ? 0.4 + 0.4 * Math.sin(frame / 4) : 0;
            return (
              <Panel
                key={col.k}
                color={col.k === "need" && n > 0 ? T.yellow : T.border}
                glow={flash}
                title={<span style={{ color: col.color }}>{col.icon} {col.title}</span>}
                right={<span style={{ color: col.k === "need" ? T.yellow : T.dim }}>{n}</span>}
                style={{ position: "absolute", left: ci * (colW + gap), top: top, width: colW, bottom: 0 }}
              />
            );
          })}

          {Object.values(state.cards).map((c) => {
            const p = glide(EVENTS.map((e) => e.f), layouts, c.id, frame, fps);
            if (!p) return null;
            const selected = state.sel === c.id;
            return (
              <div
                key={c.id}
                style={{
                  position: "absolute",
                  left: p.x,
                  top: p.y,
                  width: colW - CH * 1.2,
                  height: cardH,
                  opacity: p.o,
                  border: `2px solid ${selected ? T.cyan : T.border}`,
                  boxShadow: selected ? `0 0 22px ${T.cyan}66` : undefined,
                  background: T.bg,
                  borderRadius: 3,
                  padding: `${ROW * 0.2}px ${CH}px`,
                  overflow: "hidden",
                  whiteSpace: "nowrap",
                }}
              >
                <div style={{ display: "flex", justifyContent: "space-between" }}>
                  <span style={{ overflow: "hidden", textOverflow: "ellipsis" }}>
                    <span style={{ color: T.cyan, fontWeight: 700 }}>#{c.id}</span>{" "}
                    <span style={{ color: T.magenta }}>✧ {c.wf}</span>
                  </span>
                  {!vertical && <span style={{ color: T.dim }}>◷ now</span>}
                </div>
                <div
                  style={{
                    whiteSpace: "normal",
                    height: ROW * (vertical ? 2 : 1),
                    overflow: "hidden",
                    textOverflow: "ellipsis",
                    fontWeight: selected ? 700 : 400,
                    display: "-webkit-box",
                    WebkitLineClamp: vertical ? 2 : 1,
                    WebkitBoxOrient: "vertical",
                  }}
                >
                  {c.title}
                </div>
                <Progress c={c} />
                <div>
                  <Status c={c} frame={frame} />
                </div>
              </div>
            );
          })}

          {!vertical && sel && (
            <Panel
              title={<span style={{ color: T.cyan }}>#{sel.id}</span>}
              style={{ position: "absolute", right: 0, top, width: detailW, bottom: 0, whiteSpace: "nowrap", overflow: "hidden" }}
            >
              <div style={{ fontWeight: 700, whiteSpace: "normal" }}>{sel.title}</div>
              <div style={{ marginTop: ROW * 0.4 }}>
                {sel.st === "need" ? (
                  <Pill bg={T.yellow}>awaiting approval</Pill>
                ) : sel.st === "done" ? (
                  <Pill bg={T.green}>done</Pill>
                ) : (
                  <Pill bg={T.cyan}>running</Pill>
                )}
              </div>
              <div style={{ color: T.dim, marginTop: ROW * 0.4 }}>{sel.wf} · started 1m ago</div>
              <div style={{ marginTop: ROW * 0.5, fontWeight: 700 }}>▤ Steps</div>
              {sel.steps.map((s, i) => (
                <div key={s}>
                  <span style={{ color: i < sel.step ? T.green : i === sel.step ? T.cyan : T.dim }}>
                    {i < sel.step ? "✓" : i === sel.step ? "●" : "○"}
                  </span>{" "}
                  {i + 1} {s}
                </div>
              ))}
              {sel.st === "need" && (
                <>
                  <div style={{ marginTop: ROW * 0.5, fontWeight: 700 }}>⚡ What you can do</div>
                  <div>
                    <b style={{ color: T.cyan }}>a</b> approve <b style={{ color: T.cyan }}>x</b> reject{" "}
                    <b style={{ color: T.cyan }}>s</b> skip
                  </div>
                </>
              )}
            </Panel>
          )}
        </div>
      </Term>
      <KeyCast keys={BOARD_KEYS} />
    </div>
  );
};

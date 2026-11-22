import React from "react";
import { useCurrentFrame } from "remotion";
import { CH, Footer, Key, KeyCast, Panel, Pill, ROW, T, Term } from "../tui/kit";

// The 0.14.0 cut's chat workspace, modelled on docs/assets/tui-chat*.png:
// the chats rail on the left (landscape only), the conversation in a cyan
// panel, the composer at the bottom with `model · effort · mode` on its
// border. A marketing composite, not a capture.

export const VERSION = "0.14.0";

export const Tabs: React.FC<{ chats?: number }> = ({ chats = 4 }) => (
  <span>
    <span style={{ color: T.dim }}> ▦ Issues (3) │ ▶ Tasks (5) │ </span>
    <span style={{ background: T.pill, color: T.fg, fontWeight: 700, padding: `0 ${CH / 2}px` }}>◌ Chats ({chats})</span>
  </span>
);

/** `ctx ▰▰▰▱▱ 61%` — yellow past 80%, as the real gauge turns. */
export const Gauge: React.FC<{ pct: number }> = ({ pct }) => {
  const n = Math.round(pct / 20);
  return (
    <span style={{ color: pct > 80 ? T.yellow : T.dim }}>
      ctx {"▰".repeat(n)}
      {"▱".repeat(5 - n)} {Math.round(pct)}%
    </span>
  );
};

export const You: React.FC<{ children: React.ReactNode; faded?: boolean; tag?: string }> = ({ children, faded, tag }) => (
  <div style={{ display: "flex", justifyContent: "flex-end", marginBottom: ROW * 0.6, opacity: faded ? 0.5 : 1 }}>
    <Panel title={<span style={{ color: T.yellow }}>You</span>} right={tag && <span style={{ color: T.dim }}>{tag}</span>} style={{ maxWidth: "80%" }}>
      {children}
    </Panel>
  </div>
);

/** `◆ claude · turn 3 · ◷ 1m · 48s · 22.4k↑ 1.9k↓ · $0.07` */
export const TurnHead: React.FC<{ agent?: string; n: number; stats?: string; extra?: React.ReactNode }> = ({ agent = "claude", n, stats, extra }) => (
  <div style={{ whiteSpace: "nowrap", overflow: "hidden", textOverflow: "ellipsis" }}>
    <span style={{ color: T.magenta, fontWeight: 700 }}>◆ {agent}</span>
    <span style={{ color: T.dim }}>
      {" "}· turn {n}
      {stats ? ` · ${stats}` : ""}
    </span>
    {extra}
  </div>
);

export const Tool: React.FC<{ ok?: boolean; children: React.ReactNode }> = ({ ok = true, children }) => (
  <div style={{ whiteSpace: "nowrap", overflow: "hidden", textOverflow: "ellipsis" }}>
    <span style={{ color: ok ? T.green : T.red }}>{ok ? "✓" : "✗"}</span> {children}
  </div>
);

const Rail: React.FC<{ title: string }> = ({ title }) => (
  <Panel
    title={<span style={{ color: T.blue }}>◌ Chats</span>}
    right={<span style={{ color: T.blue }}>4</span>}
    style={{ width: "22%", whiteSpace: "nowrap", overflow: "hidden" }}
  >
    <div style={{ color: T.dim, fontWeight: 700 }}>○ Idle 3</div>
    <div style={{ border: `2px solid ${T.cyan}`, padding: `${ROW * 0.2}px ${CH}px`, margin: `${ROW * 0.3}px 0` }}>
      <div>
        <b style={{ color: T.cyan }}>#7</b> <span style={{ color: T.magenta }}>◆ claude</span>
      </div>
      <div style={{ fontWeight: 700, overflow: "hidden", textOverflow: "ellipsis" }}>{title}</div>
      <div style={{ color: T.dim }}>○ idle · 3 turns</div>
    </div>
    {[
      ["#4", "codex", "flaky upload test"],
      ["#5", "cursor", "header flicker"],
    ].map(([id, a, t]) => (
      <div key={id} style={{ padding: `${ROW * 0.2}px ${CH}px` }}>
        <div>
          <b style={{ color: T.cyan }}>{id}</b> <span style={{ color: T.magenta }}>◆ {a}</span>
        </div>
        <div style={{ color: T.dim, overflow: "hidden", textOverflow: "ellipsis" }}>{t}</div>
      </div>
    ))}
  </Panel>
);

export const Cursor: React.FC = () => {
  const frame = useCurrentFrame();
  return <span style={{ background: frame % 30 < 18 ? T.fg : "transparent", color: T.bg }}> </span>;
};

export const ChatFrame: React.FC<{
  w: number;
  h: number;
  vertical: boolean;
  keys: Key[];
  title?: string;
  state?: React.ReactNode;
  sub?: React.ReactNode;
  ctx?: number;
  draft?: React.ReactNode;
  mode?: string;
  hints?: [string, string][];
  overlay?: React.ReactNode;
  children: React.ReactNode;
}> = ({ w, h, vertical, keys, title = "per-route rate limits", state, sub, ctx, draft, mode = "opus · high · full-auto", hints, overlay, children }) => (
  <div style={{ position: "relative" }}>
    <Term
      w={w}
      h={h}
      version={VERSION}
      crumb={<Tabs />}
      footer={
        <Footer
          mode={`Chat: ${title.slice(0, 14)}…`}
          keys={keys}
          hints={
            hints ??
            (vertical
              ? [["enter", "send"], ["alt+z", "rewind"], ["alt+d", "diff"]]
              : [["enter", "send"], ["alt+z", "rewind"], ["alt+d", "diff"], ["alt+x", "shell"], ["ctrl+f", "find"], ["ctrl+p", "commands"]])
          }
        />
      }
    >
      <div style={{ display: "flex", gap: CH * 1.5, height: "100%" }}>
        {!vertical && <Rail title={title} />}
        <Panel
          color={T.cyan}
          title={
            <span>
              <span style={{ color: T.cyan }}>#7</span> {title}
            </span>
          }
          right={state ?? <Pill bg={T.pill} fg={T.fg}>idle</Pill>}
          style={{ flex: 1, minWidth: 0, overflow: "hidden" }}
        >
          <div style={{ display: "flex", flexDirection: "column", height: "100%" }}>
            <div style={{ display: "flex", justifyContent: "space-between", color: T.dim, whiteSpace: "nowrap" }}>
              <span>{sub ?? "Σ 61.4k tokens · $0.31"}</span>
              {ctx !== undefined && <Gauge pct={ctx} />}
            </div>
            <div
              style={{
                flex: 1,
                minHeight: 0,
                overflow: "hidden",
                display: "flex",
                flexDirection: "column",
                justifyContent: "flex-end",
                padding: `${ROW * 0.3}px ${CH}px`,
              }}
            >
              {children}
            </div>
            <Panel title={<span style={{ fontWeight: 400, color: T.fg }}>message · {mode}</span>} style={{ marginTop: ROW * 0.5, minHeight: ROW * 2.6 }}>
              <div style={{ background: T.sel, padding: `0 ${CH / 2}px`, whiteSpace: "pre-wrap" }}>
                {draft ?? (
                  <span style={{ color: T.dim }}>
                    <Cursor />
                    message… (enter sends)
                  </span>
                )}
              </div>
            </Panel>
          </div>
        </Panel>
      </div>
      {overlay}
    </Term>
    <KeyCast keys={keys} />
  </div>
);

/** A centred modal, drawn over the body the way the TUI's pickers are. */
export const Modal: React.FC<{ title: React.ReactNode; o: number; width?: string; children: React.ReactNode }> = ({ title, o, width = "56%", children }) => (
  <div style={{ position: "absolute", inset: 0, display: "flex", alignItems: "center", justifyContent: "center", opacity: o, pointerEvents: "none" }}>
    <div style={{ background: T.bg, width, padding: `${ROW * 0.7}px ${CH}px ${ROW * 0.3}px`, transform: `scale(${0.94 + o * 0.06})`, boxShadow: "0 30px 80px rgba(0,0,0,0.7)" }}>
      <Panel color={T.cyan} glow={0.6} title={title} style={{ background: T.bg }}>
        {children}
      </Panel>
    </div>
  </div>
);

export const Row: React.FC<{ on?: boolean; children: React.ReactNode }> = ({ on, children }) => (
  <div style={{ background: on ? T.sel : undefined, whiteSpace: "nowrap", overflow: "hidden", textOverflow: "ellipsis" }}>
    <span style={{ color: T.cyan, whiteSpace: "pre" }}>{on ? "▸ " : "  "}</span>
    {children}
  </div>
);

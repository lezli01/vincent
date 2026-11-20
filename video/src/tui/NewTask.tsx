import React from "react";
import { useCurrentFrame, useVideoConfig } from "remotion";
import { CH, Footer, Key, KeyCast, Panel, Pill, ROW, T, Term, typed, useSpringAt } from "./kit";

export const NT_KEYS: Key[] = [
  { f: 58, key: "enter", label: "pick an agent" },
  { f: 70, key: "↓", label: "claude" },
  { f: 80, key: "↓", label: "codex" },
  { f: 90, key: "↓", label: "cursor" },
  { f: 102, key: "enter", label: "use cursor" },
  { f: 116, key: "ctrl+s", label: "create task" },
];

const AGENTS = [
  ["(workflow default)", ""],
  ["claude", "Claude Code"],
  ["codex", "OpenAI Codex"],
  ["cursor", "Cursor"],
  ["copilot", "GitHub Copilot"],
];

const TITLE = "cap the public API per token per minute";
const DESC = "Return 429 with a Retry-After header.";

export const NewTask: React.FC<{ w: number; h: number; vertical: boolean }> = ({ w, h, vertical }) => {
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  const pickerOpen = frame >= 58 && frame < 102;
  const cursorIdx = frame < 70 ? 0 : frame < 80 ? 1 : frame < 90 ? 2 : 3;
  const agent = frame >= 102 ? "cursor" : "(workflow default)";
  const created = frame >= 116;
  const toast = useSpringAt(118);
  const title = typed(TITLE, frame, 4, 34, fps);
  const desc = typed(DESC, frame, 36, 40, fps);
  const caret = Math.floor(frame / 8) % 2 === 0 ? "▋" : " ";
  const field = (label: string, value: React.ReactNode, active: boolean, hint?: string) => (
    <Panel
      title={<span style={{ color: active ? T.cyan : T.fg }}>{label}</span>}
      right={hint ? <span style={{ color: T.dim, fontWeight: 400 }}>{hint}</span> : undefined}
      color={active ? T.cyan : T.border}
      style={{ marginBottom: ROW * 0.7, whiteSpace: "nowrap", overflow: "hidden" }}
    >
      {value}
    </Panel>
  );

  const startRows: [string, React.ReactNode][] = [
    ["priority", "– 80 +"],
    ["start", <span key="s">now</span>],
    ["agent", <span key="a" style={{ color: agent === "cursor" ? T.cyan : T.dim, fontWeight: agent === "cursor" ? 700 : 400 }}>{agent}</span>],
    ["model", <span key="m" style={{ color: T.dim }}>(workflow default)</span>],
  ];

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
            mode="New task"
            keys={NT_KEYS}
            hints={vertical ? [["ctrl+s", "create"], ["enter", "edit field"], ["↓", "next"]] : [["ctrl+s", "create"], ["enter", "edit field"], ["↓", "next"], ["←", "workflows"], ["e", "$EDITOR"], ["+/-", "priority"], ["?", "help"]]}
          />
        }
      >
        <div style={{ display: "flex", gap: CH * 1.5, height: "100%" }}>
          {!vertical && (
            <Panel
              title={<span style={{ color: T.cyan }}>✧ Workflow</span>}
              right={<span style={{ color: T.cyan }}>18</span>}
              style={{ width: "21%", whiteSpace: "nowrap", overflow: "hidden" }}
            >
              {["change-request", "docs-refresh", "feature-pr", "incident-response", "platform-upgrade"].map((n) => (
                <div key={n} style={{ border: `2px solid ${n === "feature-pr" ? T.cyan : T.border}`, padding: `0 ${CH}px`, marginBottom: ROW * 0.35, fontWeight: n === "feature-pr" ? 700 : 400 }}>
                  <span style={{ color: T.magenta }}>✧</span> {n}
                </div>
              ))}
            </Panel>
          )}
          <Panel
            title={<span>▶ New task · <span style={{ color: T.magenta }}>feature-pr</span></span>}
            right={title.length === TITLE.length ? <Pill bg={T.green}>✓ ready</Pill> : <span style={{ color: T.dim }}>…</span>}
            style={{ flex: 1, position: "relative", paddingTop: ROW }}
          >
            {field("Title", <span>{title}{frame < 36 && <span style={{ color: T.cyan }}>{caret}</span>}</span>, frame < 36, vertical ? undefined : "what should the agent do?")}
            {field("≡ Description", <span>{desc || <span style={{ color: T.dim }}>optional</span>}{frame >= 36 && frame < 58 && <span style={{ color: T.cyan }}>{caret}</span>}</span>, frame >= 36 && frame < 58)}
            <Panel
              title={<span style={{ color: pickerOpen ? T.cyan : T.fg }}>◷ Start</span>}
              color={frame >= 56 && frame < 116 ? T.cyan : T.border}
              style={{ whiteSpace: "nowrap" }}
            >
              {startRows.map(([k, v]) => (
                <div key={k} style={{ display: "flex" }}>
                  <span style={{ color: k === "agent" && frame >= 56 ? T.cyan : T.dim, width: CH * 12 }}>
                    {k === "agent" && frame >= 56 ? "› " : "  "}
                    {k}
                  </span>
                  {v}
                </div>
              ))}
            </Panel>
            {pickerOpen && (
              <Panel
                title={<span style={{ color: T.cyan }}>agent override</span>}
                color={T.cyan}
                style={{ marginTop: ROW * 0.7, background: T.bg, whiteSpace: "nowrap" }}
              >
                {AGENTS.map(([id, name], i) => (
                  <div key={id} style={{ background: i === cursorIdx ? T.sel : undefined, fontWeight: i === cursorIdx ? 700 : 400 }}>
                    <span style={{ color: T.cyan }}>{i === cursorIdx ? "▸ " : "  "}</span>
                    {id} <span style={{ color: T.dim }}>{name}</span>
                    {i > 0 && <span style={{ color: T.green }}> ✓ ready</span>}
                  </div>
                ))}
              </Panel>
            )}
            <div style={{ marginTop: ROW * 0.8, color: created ? T.bg : T.cyan, fontWeight: 700 }}>
              <span style={{ background: created ? T.cyan : undefined, padding: `0 ${CH / 2}px` }}>[ create task ]</span>
              <span style={{ color: T.dim, fontWeight: 400 }}> ctrl+s from anywhere</span>
            </div>
            {created && (
              <div
                style={{
                  position: "absolute",
                  right: CH * 2,
                  bottom: ROW * 0.8,
                  border: `2px solid ${T.green}`,
                  background: T.bg,
                  padding: `${ROW * 0.3}px ${CH * 1.5}px`,
                  color: T.green,
                  fontWeight: 700,
                  transform: `translateY(${(1 - toast) * 30}px)`,
                  opacity: toast,
                  boxShadow: `0 0 24px ${T.green}66`,
                }}
              >
                ✓ task #32 queued · cursor
              </div>
            )}
          </Panel>
          {!vertical && (
            <Panel title={<span>What will run</span>} style={{ width: "24%", whiteSpace: "nowrap", overflow: "hidden" }}>
              <div style={{ fontWeight: 700 }}>▶ Steps</div>
              {[
                ["◆", "implement", agent === "cursor" ? "cursor" : "claude"],
                ["$", "commit", "runs a command"],
                ["॥", "review", "waits for you"],
                ["$", "publish", "runs a command"],
              ].map(([g, n, d], i) => (
                <div key={n}>
                  <span style={{ color: T.dim }}>{i + 1}</span> <span style={{ color: T.magenta }}>{g}</span> {n}
                  <div style={{ color: i === 0 && agent === "cursor" ? T.cyan : T.dim, paddingLeft: CH * 4 }}>{d}</div>
                </div>
              ))}
              <div style={{ marginTop: ROW * 0.5, fontWeight: 700 }}>⌥ Where it runs</div>
              <div style={{ color: T.dim }}>a new worktree</div>
              <div style={{ color: T.dim }}>on its own branch</div>
            </Panel>
          )}
        </div>
      </Term>
      <KeyCast keys={NT_KEYS} />
    </div>
  );
};

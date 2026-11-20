import React from "react";
import { interpolate, useCurrentFrame } from "remotion";
import { CH, clamp, Footer, Key, KeyCast, Panel, Pill, ROW, Spinner, T, Term } from "./kit";

export const ENGINE_KEYS: Key[] = [{ f: 132, key: "a", label: "approve" }];

type Kind = "agent" | "command" | "manual";
type Step = { id: string; kind: Kind; run: [number, number]; tokens?: number; secs?: string; note?: string };

// The point of the scene: only the steps that need judgement pay for inference.
const STEPS: Step[] = [
  { id: "implement", kind: "agent", run: [10, 54], tokens: 14.8, note: "check: go test ✓" },
  { id: "lint", kind: "command", run: [54, 66], secs: "1.2s" },
  { id: "test", kind: "command", run: [66, 80], secs: "3.8s" },
  { id: "review", kind: "agent", run: [80, 112], tokens: 3.4 },
  { id: "approve", kind: "manual", run: [112, 134] },
  { id: "publish", kind: "command", run: [134, 146], secs: "0.9s" },
];

const BADGE: Record<Kind, [string, string]> = {
  agent: ["◆ inference", T.magenta],
  command: ["$ no inference", T.green],
  manual: ["॥ you decide", T.yellow],
};

// [text, colour, step it belongs to]
const Y = (k: string, v: React.ReactNode, ind = 4) => (
  <>
    {" ".repeat(ind)}
    <span style={{ color: T.blue }}>{k}:</span> {v}
  </>
);
const tpl = (s: string) => <span style={{ color: T.cyan }}>{s}</span>;
const kind = (k: Kind) => <span style={{ color: BADGE[k][1], fontWeight: 700 }}>{k}</span>;

const YAML: [React.ReactNode, string?][] = [
  [Y("name", "ship-feature", 0)],
  [Y("defaults", "{ agent: claude, max_retries: 2 }", 0)],
  [Y("steps", "", 0)],
  [<>  - {Y("id", "implement", 0)}</>, "implement"],
  [Y("type", kind("agent")), "implement"],
  [Y("prompt", <>Implement {tpl("{{.Task.Title}}")}.</>), "implement"],
  [Y("check", "go build ./... && go test ./..."), "implement"],
  [<>  - {Y("id", "lint", 0)}</>, "lint"],
  [Y("type", kind("command")), "lint"],
  [Y("run", "golangci-lint run"), "lint"],
  [<>  - {Y("id", "test", 0)}</>, "test"],
  [Y("type", kind("command")), "test"],
  [Y("run", "go test -race ./..."), "test"],
  [<>  - {Y("id", "review", 0)}</>, "review"],
  [Y("type", kind("agent")), "review"],
  [Y("prompt", "Review the diff for bugs."), "review"],
  [<>  - {Y("id", "approve", 0)}</>, "approve"],
  [Y("type", kind("manual")), "approve"],
  [<>  - {Y("id", "publish", 0)}</>, "publish"],
  [Y("type", kind("command")), "publish"],
  [Y("run", <>git push -u origin {tpl("{{.Task.BranchName}}")}</>), "publish"],
];

export const Engine: React.FC<{ w: number; h: number; vertical: boolean }> = ({ w, h, vertical }) => {
  const frame = useCurrentFrame();
  const current = STEPS.find((s) => frame >= s.run[0] && frame < s.run[1])?.id;
  const tokensOf = (s: Step) => (s.tokens ? s.tokens * interpolate(frame, s.run, [0, 1], clamp) : 0);
  const total = STEPS.reduce((n, s) => n + tokensOf(s), 0);
  const cost = total * 0.0115;

  const yaml = (
    <Panel
      title={<span style={{ color: T.cyan }}>.vincent/workflows/ship-feature.yaml</span>}
      right={<span style={{ color: T.green }}>✓ valid</span>}
      style={{ flex: vertical ? "0 0 auto" : "0 0 46%", whiteSpace: "pre", overflow: "hidden", fontSize: vertical ? 17 : 18, lineHeight: 1.3 }}
    >
      {YAML.filter((_, i) => !vertical || i !== 1).map(([line, step], i) => {
        const on = step && step === current;
        return (
          <div key={i} style={{ background: on ? T.sel : undefined, boxShadow: on ? `inset 3px 0 0 ${T.cyan}` : undefined }}>
            {line}
          </div>
        );
      })}
    </Panel>
  );

  const run = (
    <Panel
      title={<span style={{ color: T.cyan }}>▶ Run · task #40</span>}
      right={frame >= 146 ? <Pill bg={T.green}>done</Pill> : <span style={{ color: T.dim }}>live</span>}
      style={{ flex: 1, whiteSpace: "nowrap", overflow: "hidden", display: "flex", flexDirection: "column" }}
    >
      {STEPS.map((s) => {
        const st = frame < s.run[0] ? "todo" : frame < s.run[1] ? "run" : "done";
        const [label, color] = BADGE[s.kind];
        return (
          <div
            key={s.id}
            style={{
              display: "flex",
              justifyContent: "space-between",
              alignItems: "center",
              padding: `${ROW * 0.12}px ${CH / 2}px`,
              marginBottom: ROW * 0.18,
              background: st === "run" ? T.sel : undefined,
              opacity: st === "todo" ? 0.5 : 1,
            }}
          >
            <span>
              <span style={{ display: "inline-block", width: CH * 2 }}>
                {st === "done" ? (
                  <span style={{ color: T.green }}>✓</span>
                ) : st === "run" ? (
                  s.kind === "manual" ? <span style={{ color: T.yellow }}>!</span> : <Spinner />
                ) : (
                  <span style={{ color: T.dim }}>○</span>
                )}
              </span>
              <b>{s.id}</b>{" "}
              <span style={{ color, border: `1px solid ${color}`, padding: `0 ${CH / 2}px`, borderRadius: 3, fontSize: "0.85em" }}>
                {label}
              </span>
            </span>
            <span style={{ color: T.dim }}>
              {s.kind === "agent" && st !== "todo" && (
                <span style={{ color: T.magenta }}>{tokensOf(s).toFixed(1)}k tok</span>
              )}
              {s.kind === "command" && st === "done" && (
                <span>
                  0 tok · {s.secs}
                </span>
              )}
              {s.kind === "manual" && st === "run" && <span style={{ color: T.yellow }}>waiting for you</span>}
              {s.kind === "manual" && st === "done" && <span style={{ color: T.green }}>approved</span>}
            </span>
          </div>
        );
      })}
      <div style={{ flex: 1 }} />
      <div style={{ borderTop: `1px solid ${T.border}`, paddingTop: ROW * 0.3, whiteSpace: "normal" }}>
        <div>
          <span style={{ color: T.magenta, fontWeight: 700 }}>◆ inference</span>{" "}
          <b>2</b> of 6 steps · <span style={{ color: T.magenta }}>{total.toFixed(1)}k tokens</span> · ${cost.toFixed(2)}
        </div>
        <div>
          <span style={{ color: T.green, fontWeight: 700 }}>$ deterministic</span> <b>3</b> steps ·{" "}
          <span style={{ color: T.green }}>0 tokens · $0.00</span>
        </div>
      </div>
    </Panel>
  );

  return (
    <div style={{ position: "relative" }}>
      <Term
        w={w}
        h={h}
        crumb={<span style={{ color: T.dim }}> Tasks › #40 › Workflow</span>}
        footer={
          <Footer
            mode="Task #40"
            keys={ENGINE_KEYS}
            hints={vertical ? [["a", "approve"], ["5", "workflow"], ["r", "retry"]] : [["tab", "tabs"], ["5", "workflow"], ["a", "approve"], ["r", "retry"], ["E", "edit+retry"], ["?", "help"]]}
          />
        }
      >
        <div style={{ display: "flex", flexDirection: vertical ? "column" : "row", gap: ROW * 0.8, height: "100%" }}>
          {yaml}
          {run}
        </div>
      </Term>
      <KeyCast keys={ENGINE_KEYS} />
    </div>
  );
};

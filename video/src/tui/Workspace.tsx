import React from "react";
import { useCurrentFrame } from "remotion";
import { CH, Footer, Key, KeyCast, Panel, Pill, ROW, Spinner, T, Term } from "./kit";

export const WS_KEYS: Key[] = [{ f: 88, key: "4", label: "diff" }];

type Line = { f: number; node: React.ReactNode };

const OUTPUT: Line[] = [
  { f: 6, node: <span style={{ color: T.blue }}>● Read internal/api/server.go</span> },
  { f: 14, node: <span>I'll add a token-bucket limiter as middleware.</span> },
  { f: 24, node: <span style={{ color: T.blue }}>● Write internal/api/ratelimit.go <span style={{ color: T.green }}>+48</span></span> },
  { f: 34, node: <span style={{ color: T.blue }}>● Edit internal/api/server.go <span style={{ color: T.green }}>+3</span> <span style={{ color: T.red }}>-1</span></span> },
  { f: 44, node: <span style={{ color: T.blue }}>● Bash go test ./internal/api/...</span> },
  { f: 58, node: <span style={{ color: T.green }}>  ok  internal/api  0.412s</span> },
  { f: 66, node: <span>Done: 100 req/min per token, 429 with Retry-After.</span> },
];

const DIFF: Line[] = [
  { f: 0, node: <span style={{ color: T.dim }}>2 files <span style={{ color: T.green }}>+51</span> <span style={{ color: T.red }}>-1</span></span> },
  { f: 2, node: <span style={{ background: T.sel, fontWeight: 700 }}>▾ internal/api/ratelimit.go <span style={{ color: T.green }}>+48</span></span> },
  { f: 5, node: <span style={{ color: T.cyan }}>@@ -0,0 +1,48 @@</span> },
  { f: 7, node: <span style={{ color: T.green }}>+package api</span> },
  { f: 9, node: <span style={{ color: T.green }}>+// limiter is one token bucket per caller.</span> },
  { f: 11, node: <span style={{ color: T.green }}>+type limiter struct {"{"}</span> },
  { f: 13, node: <span style={{ color: T.green }}>+    rate, burst int</span> },
  { f: 15, node: <span style={{ color: T.green }}>+    buckets map[string]*bucket</span> },
  { f: 17, node: <span style={{ color: T.green }}>+{"}"}</span> },
  { f: 20, node: <span style={{ fontWeight: 700 }}>▾ internal/api/server.go <span style={{ color: T.green }}>+3</span> <span style={{ color: T.red }}>-1</span></span> },
  { f: 22, node: <span style={{ color: T.cyan }}>@@ -41,7 +41,9 @@</span> },
  { f: 24, node: <span style={{ color: T.red }}>-    mux.Handle("/v1/", api)</span> },
  { f: 26, node: <span style={{ color: T.green }}>+    lim := newLimiter(100, 20)</span> },
  { f: 28, node: <span style={{ color: T.green }}>+    mux.Handle("/v1/", lim.wrap(api))</span> },
];

const STEPS = ["implement", "commit", "review", "publish"];

export const Workspace: React.FC<{ w: number; h: number; vertical: boolean }> = ({ w, h, vertical }) => {
  const frame = useCurrentFrame();
  const diff = frame >= WS_KEYS[0].f;
  const step = frame < 74 ? 0 : frame < 104 ? 1 : 2;
  const lines = diff ? DIFF.map((l) => ({ ...l, f: l.f + WS_KEYS[0].f + 2 })) : OUTPUT;
  const shown = lines.filter((l) => frame >= l.f);
  const branch = "feat/1-add-rate-limiting";

  const stateOf = (i: number) => (i < step ? "done" : i === step ? (step === 2 ? "gate" : "run") : "todo");

  return (
    <div style={{ position: "relative" }}>
      <Term
        w={w}
        h={h}
        crumb={<span style={{ color: T.dim }}> Tasks › #1 › {diff ? "Diff" : "Output"}</span>}
        footer={
          <Footer
            mode="Task #1"
            keys={WS_KEYS}
            hints={
              vertical
                ? [["tab", "tabs"], ["4", "diff"], ["r", "retry"], ["T", "chat"]]
                : [["tab", "tabs"], ["3", "output"], ["4", "diff"], ["r", "retry"], ["E", "edit+retry"], ["c", "cancel"], ["T", "chat"], ["?", "help"]]
            }
          />
        }
      >
        <div style={{ display: "flex", flexDirection: "column", gap: ROW * 0.8, height: "100%" }}>
          <Panel style={{ whiteSpace: "nowrap" }}>
            <div style={{ display: "flex", justifyContent: "space-between" }}>
              <span>
                <b style={{ color: T.cyan }}>#1</b> <b>add rate limiting to the public API</b>
              </span>
              {!vertical &&
                (step === 2 ? <Pill bg={T.yellow}>awaiting approval</Pill> : <Pill bg={T.cyan}>running</Pill>)}
            </div>
            <div style={{ color: T.dim }}>
              step {step + 1} of 4 · feature-pr{vertical ? "" : " · "}
              {vertical && <br />}
              <span style={{ color: T.green, fontWeight: 700 }}>⎇ {branch}</span>
              <span style={{ color: T.dim }}> · own worktree</span>
            </div>
            {vertical && (
              <div style={{ marginTop: ROW * 0.2 }}>
                {step === 2 ? <Pill bg={T.yellow}>awaiting approval</Pill> : <Pill bg={T.cyan}>running</Pill>}
              </div>
            )}
          </Panel>
          <div style={{ display: "flex", gap: CH * 1.5, flex: 1, minHeight: 0 }}>
            <Panel
              title={<span style={{ color: T.blue }}>▤ Steps</span>}
              right={<span style={{ color: T.blue }}>4</span>}
              style={{ width: vertical ? "34%" : "24%", whiteSpace: "nowrap", overflow: "hidden" }}
            >
              {STEPS.map((s, i) => {
                const st = stateOf(i);
                return (
                  <div key={s} style={{ marginBottom: ROW * 0.5 }}>
                    <div style={{ color: T.cyan, fontWeight: 700 }}>
                      Step {i + 1} {s}
                    </div>
                    <div style={{ paddingLeft: CH * 2, background: st === "run" || st === "gate" ? T.sel : undefined }}>
                      {st === "done" && <span style={{ color: T.green }}>✓ succeeded</span>}
                      {st === "run" && (
                        <span style={{ color: T.cyan, fontWeight: 700 }}>
                          <Spinner /> running
                        </span>
                      )}
                      {st === "gate" && <span style={{ color: T.yellow, fontWeight: 700 }}>! awaiting you</span>}
                      {st === "todo" && <span style={{ color: T.dim }}>○ pending</span>}
                      {i === 0 && st !== "todo" && !vertical && <span style={{ color: T.dim }}> claude</span>}
                    </div>
                  </div>
                );
              })}
            </Panel>
            <Panel color={T.cyan} style={{ flex: 1, overflow: "hidden", whiteSpace: "nowrap" }}>
              <div style={{ display: "flex", gap: CH * 1.5, borderBottom: `1px solid ${T.border}`, paddingBottom: ROW * 0.2, marginBottom: ROW * 0.4 }}>
                {[
                  ["3", "Output", !diff],
                  ["4", "Diff", diff],
                  ["0", "Overview", false],
                  ["5", "Workflow", false],
                ].map(([n, l, on]) => (
                  <span key={l as string} style={{ color: on ? T.fg : T.dim, fontWeight: on ? 700 : 400, borderBottom: on ? `3px solid ${T.cyan}` : undefined }}>
                    {n} {l}
                  </span>
                ))}
              </div>
              {shown.map((l, i) => (
                <div key={(diff ? "d" : "o") + i} style={{ opacity: Math.min(1, (frame - l.f) / 4), overflow: "hidden", textOverflow: "ellipsis" }}>
                  {l.node}
                </div>
              ))}
              {!diff && frame < 70 && (
                <div>
                  <Spinner /> <span style={{ color: T.dim }}>claude is working…</span>
                </div>
              )}
            </Panel>
          </div>
        </div>
      </Term>
      <KeyCast keys={WS_KEYS} />
    </div>
  );
};

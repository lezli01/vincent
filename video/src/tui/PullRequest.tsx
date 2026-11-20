import React from "react";
import { useCurrentFrame } from "remotion";
import { CH, Footer, Key, KeyCast, Panel, Pill, ROW, Spinner, T, Term, useSpringAt } from "./kit";

export const PR_KEYS: Key[] = [
  { f: 8, key: "P", label: "push & open a pull request" },
  { f: 86, key: "m", label: "merge" },
  { f: 100, key: "y", label: "confirm" },
];

const CHECKS: [string, number][] = [
  ["license/cla", 0],
  ["lint", 48],
  ["test", 60],
  ["build", 72],
];

export const PullRequest: React.FC<{ w: number; h: number; vertical: boolean }> = ({ w, h, vertical }) => {
  const frame = useCurrentFrame();
  const pushing = frame >= 8 && frame < 30;
  const opened = frame >= 30;
  const confirm = frame >= 86 && frame < 100;
  const merged = frame >= 100;
  const pop = useSpringAt(30);
  const mergePop = useSpringAt(100, 10);
  const branch = "feat/2-bump-the-design-tokens";

  return (
    <div style={{ position: "relative" }}>
      <Term
        w={w}
        h={h}
        crumb={<span style={{ color: T.dim }}> Tasks › #2 › Pull Request</span>}
        footer={
          <Footer
            mode="Task #2"
            keys={PR_KEYS}
            hints={vertical ? [["P", "open a PR"], ["m", "merge"], ["y", "confirm"]] : [["tab", "tabs"], ["P", "open a PR"], ["m", "merge"], ["y", "confirm"], ["o", "browser"], ["R", "re-run checks"], ["?", "help"]]}
          />
        }
      >
        <div style={{ display: "flex", flexDirection: "column", gap: ROW * 0.8, height: "100%" }}>
          <Panel style={{ whiteSpace: "nowrap" }}>
            <div style={{ display: "flex", justifyContent: "space-between" }}>
              <span>
                <b style={{ color: T.cyan }}>#2</b> <b>bump the design tokens</b>
              </span>
              <Pill bg={T.green}>done</Pill>
            </div>
            <div style={{ color: T.dim }}>
              step 4 of 4 · feature-pr · <span style={{ color: T.green }}>⎇ {branch}</span>
            </div>
          </Panel>
          <Panel color={T.cyan} style={{ flex: 1, whiteSpace: "nowrap", overflow: "hidden", position: "relative" }}>
            <div style={{ display: "flex", gap: CH * 1.5, borderBottom: `1px solid ${T.border}`, paddingBottom: ROW * 0.2, marginBottom: ROW * 0.6 }}>
              {[["3", "Output"], ["4", "Diff"], ["0", "Overview"], ...(vertical ? [] : [["5", "Workflow"]]), ["7", "Pull Request"]].map(([n, l]) => {
                const on = l === "Pull Request";
                return (
                  <span key={l} style={{ color: on ? T.fg : T.dim, fontWeight: on ? 700 : 400, borderBottom: on ? `3px solid ${T.cyan}` : undefined }}>
                    {n} {l}
                  </span>
                );
              })}
            </div>

            {!opened && !pushing && (
              <div style={{ color: T.dim }}>
                No pull request yet. <b style={{ color: T.cyan }}>P</b> pushes the branch and opens one.
              </div>
            )}
            {pushing && (
              <div>
                <Spinner /> pushing <span style={{ color: T.green }}>{branch}</span> to origin…
                {frame >= 18 && (
                  <div>
                    <Spinner /> gh pr create --base main
                  </div>
                )}
              </div>
            )}
            {opened && (
              <div style={{ opacity: pop, transform: `translateY(${(1 - pop) * 20}px)` }}>
                <div>
                  <b style={{ color: T.cyan }}>#412</b>{" "}
                  <span style={{ transform: `scale(${merged ? 0.8 + mergePop * 0.2 : 1})`, display: "inline-block" }}>
                    {merged ? <Pill bg={T.magenta}>merged</Pill> : <Pill bg={T.green}>open</Pill>}
                  </span>{" "}
                  <b>Bump the design tokens</b>
                </div>
                <div style={{ color: T.cyan }}>
                  ⌥ {branch} <span style={{ color: T.dim }}>→</span> main
                </div>
                <div style={{ marginTop: ROW * 0.6, fontWeight: 700 }}>✓ Checks</div>
                {CHECKS.map(([name, at]) => {
                  const ok = frame >= at + 30 || at === 0;
                  return (
                    <div key={name}>
                      {ok ? <span style={{ color: T.green }}>✓</span> : <Spinner color={T.yellow} />}{" "}
                      <span style={{ color: ok ? T.fg : T.dim }}>{name}</span>
                    </div>
                  );
                })}
                {frame >= 102 && (
                  <div style={{ marginTop: ROW * 0.6, color: T.magenta, fontWeight: 700 }}>⇄ merged into main</div>
                )}
                {frame >= 72 + 30 - 0 && frame < 86 && <div style={{ marginTop: ROW * 0.6, color: T.green }}>all checks passed</div>}
              </div>
            )}

            {confirm && (
              <Panel
                title={<span style={{ color: T.cyan }}>merge a pull request</span>}
                color={T.cyan}
                glow={0.6}
                style={{
                  position: "absolute",
                  left: "50%",
                  top: "45%",
                  transform: "translate(-50%, -50%)",
                  background: T.bg,
                  minWidth: CH * (vertical ? 34 : 44),
                }}
              >
                <div>Merge <b style={{ color: T.cyan }}>#412</b> into main?</div>
                <div style={{ color: T.dim }}>
                  method <span style={{ color: T.fg }}>‹ merge ›</span> squash rebase
                </div>
                <div style={{ marginTop: ROW * 0.3 }}>
                  <b style={{ color: T.cyan }}>y</b> merge · <b style={{ color: T.cyan }}>n</b> cancel
                </div>
              </Panel>
            )}
          </Panel>
        </div>
      </Term>
      <KeyCast keys={PR_KEYS} />
    </div>
  );
};

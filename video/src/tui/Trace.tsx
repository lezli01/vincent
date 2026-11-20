import React from "react";
import { useCurrentFrame, useVideoConfig } from "remotion";
import { CH, Footer, Key, KeyCast, Panel, ROW, T, Term, typed } from "./kit";

export const TRACE_KEYS: Key[] = [{ f: 80, key: "enter", label: "open attempt 2" }];

// [time, what, colour]
const EVENTS: [string, React.ReactNode, string?][] = [
  ["14:02:11", "task #40 created · ship-feature"],
  ["14:02:11", "admitted · slot 2 of 4"],
  ["14:02:12", "worktree vincent/40-rate-limit"],
  ["14:02:13", "implement · attempt 1 · claude"],
  ["14:04:01", "check failed · go test: 2 failures", T.red],
  ["14:04:02", "implement · attempt 2 · retry with the failure", T.yellow],
  ["14:06:16", "check passed · go test ✓", T.green],
  ["14:06:17", "lint · command · exit 0 · 1.2s"],
  ["14:06:21", "test · command · exit 0 · 3.8s"],
  ["14:06:22", "review · attempt 1 · claude"],
  ["14:07:05", "approve · waiting for you", T.yellow],
  ["14:09:40", "approved by you", T.green],
  ["14:09:41", "publish · exit 0 · pushed", T.green],
];

const SEL = 5; // the retry row

export const Trace: React.FC<{ w: number; h: number; vertical: boolean }> = ({ w, h, vertical }) => {
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  const open = frame >= TRACE_KEYS[0].f;
  const cmd = "vincent task transcript 40 --json | jq";
  const shown = vertical ? EVENTS.slice(0, open ? EVENTS.length : EVENTS.length) : EVENTS;

  const log = (
    <Panel
      title={<span style={{ color: T.cyan }}>≡ Event log · task #40</span>}
      right={<span style={{ color: T.dim }}>durable</span>}
      style={{ flex: vertical ? "0 0 auto" : "0 0 50%", whiteSpace: "nowrap", overflow: "hidden" }}
    >
      {shown.map(([t, what, color], i) => {
        const at = 4 + i * 5;
        if (frame < at) return null;
        const sel = open && i === SEL;
        return (
          <div
            key={i}
            style={{
              opacity: Math.min(1, (frame - at) / 4),
              background: sel ? T.sel : undefined,
              boxShadow: sel ? `inset 3px 0 0 ${T.cyan}` : undefined,
              overflow: "hidden",
              textOverflow: "ellipsis",
              fontSize: vertical ? 18 : undefined,
            }}
          >
            <span style={{ color: T.dim }}>{t}</span> <span style={{ color: color ?? T.fg }}>{what}</span>
          </div>
        );
      })}
    </Panel>
  );

  const kv = (k: string, v: React.ReactNode) => (
    <div style={{ display: "flex" }}>
      <span style={{ color: T.dim, width: CH * 11, flexShrink: 0 }}>{k}</span>
      <span style={{ overflow: "hidden", textOverflow: "ellipsis" }}>{v}</span>
    </div>
  );

  const detail = (
    <Panel
      color={open ? T.cyan : T.border}
      title={<span style={{ color: T.cyan }}>⌕ Step details</span>}
      style={{ flex: 1, whiteSpace: "nowrap", overflow: "hidden" }}
    >
      {!open ? (
        <div style={{ color: T.dim }}>select an event to see the attempt behind it</div>
      ) : (
        <div style={{ opacity: Math.min(1, (frame - TRACE_KEYS[0].f) / 5) }}>
          <div style={{ fontWeight: 700 }}>
            implement · attempt 2 of 3 <span style={{ color: T.green }}>✓ succeeded</span>
          </div>
          {kv("agent", "claude · sonnet · full-auto")}
          {kv("duration", "2m14s")}
          {kv("tokens", <span style={{ color: T.magenta }}>12.1k in · 2.7k out</span>)}
          {kv("cost", "$0.17")}
          {kv("check", <span style={{ color: T.green }}>go build && go test ✓</span>)}
          {kv("prompt", <span style={{ color: T.fg }}>Implement add rate limiting…</span>)}
          {kv("", <span style={{ color: T.yellow }}>+ previous attempt failed: --- FAIL TestLimiter</span>)}
          {kv("transcript", <span style={{ color: T.blue }}>transcripts/40/implement-2.jsonl</span>)}
          <div style={{ marginTop: ROW * 0.5, borderTop: `1px solid ${T.border}`, paddingTop: ROW * 0.3 }}>
            <span style={{ color: T.green }}>$ </span>
            {typed(cmd, frame, TRACE_KEYS[0].f + 8, 40, fps)}
            {frame >= TRACE_KEYS[0].f + 36 && (
              <>
                <div style={{ color: T.dim }}>{'{"type":"tool_use","name":"Edit","file":"ratelimit.go"}'}</div>
                <div style={{ color: T.dim }}>{'{"type":"result","usage":{"input":12104,"output":2711}}'}</div>
              </>
            )}
          </div>
        </div>
      )}
    </Panel>
  );

  return (
    <div style={{ position: "relative" }}>
      <Term
        w={w}
        h={h}
        crumb={<span style={{ color: T.dim }}> Tasks › #40 › Overview</span>}
        footer={
          <Footer
            mode="Task #40"
            keys={TRACE_KEYS}
            hints={vertical ? [["enter", "details"], ["3", "output"], ["r", "retry"]] : [["↑↓", "events"], ["enter", "details"], ["3", "output"], ["4", "diff"], ["r", "retry"], ["?", "help"]]}
          />
        }
      >
        <div style={{ display: "flex", flexDirection: vertical ? "column" : "row", gap: ROW * 0.8, height: "100%" }}>
          {log}
          {detail}
        </div>
      </Term>
      <KeyCast keys={TRACE_KEYS} />
    </div>
  );
};

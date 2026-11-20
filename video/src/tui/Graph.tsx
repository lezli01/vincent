import React from "react";
import { interpolate, useCurrentFrame } from "remotion";
import { CH, clamp, Footer, Key, KeyCast, Panel, Pill, ROW, Spinner, T, Term } from "./kit";

export const GRAPH_KEYS: Key[] = [
  { f: 6, key: "5", label: "workflow tab" },
  { f: 118, key: "a", label: "approve the merge" },
];

// [start, end] frame of each node's run; a node is pending before, running
// between, and done after.
const RUN: Record<string, [number, number]> = {
  plan: [8, 26],
  fan: [26, 30],
  api_impl: [30, 58],
  api_test: [58, 80],
  tui_impl: [30, 92],
  docs: [30, 52],
  release: [30, 74],
  approve: [96, 118],
};

type NodeSt = "todo" | "run" | "done" | "gate";
const stOf = (id: string, f: number): NodeSt => {
  const [a, b] = RUN[id];
  if (f < a) return "todo";
  if (f < b) return id === "approve" ? "gate" : "run";
  return "done";
};

const LANES = [
  { id: "api_impl", lane: "api", type: "agent · claude" },
  { id: "tui_impl", lane: "tui", type: "agent · codex" },
  { id: "docs", lane: "docs", type: "workflow" },
  { id: "release", lane: "package", type: "workflow" },
];

const Node: React.FC<{ x: number; y: number; w: number; h: number; name: string; type: string; id: string; frame: number }> = ({
  x, y, w, h, name, type, id, frame,
}) => {
  const st = stOf(id, frame);
  const [a, b] = RUN[id];
  const p = interpolate(frame, [a, b], [0, 1], clamp);
  const color = st === "done" ? T.green : st === "run" ? T.cyan : st === "gate" ? T.yellow : T.border;
  const pulse = st === "run" || st === "gate" ? 0.5 + 0.5 * Math.sin(frame / 3) : 0;
  return (
    <div
      style={{
        position: "absolute",
        left: x - w / 2,
        top: y,
        width: w,
        height: h,
        border: `2px solid ${color}`,
        boxShadow: pulse ? `0 0 ${10 + 18 * pulse}px ${color}88` : undefined,
        background: T.bg,
        padding: `${ROW * 0.15}px ${CH}px`,
        whiteSpace: "nowrap",
        overflow: "hidden",
      }}
    >
      <div style={{ display: "flex", justifyContent: "space-between" }}>
        <span style={{ fontWeight: 700, color: st === "todo" ? T.dim : T.fg }}>{name}</span>
        <span>
          {st === "done" && <span style={{ color: T.green }}>✓</span>}
          {st === "run" && <Spinner />}
          {st === "gate" && <span style={{ color: T.yellow }}>!</span>}
        </span>
      </div>
      <div style={{ color: st === "gate" ? T.yellow : T.dim }}>{st === "gate" ? "waiting for you" : type}</div>
      {st !== "todo" && id !== "approve" && (
        <div style={{ position: "absolute", left: 0, bottom: 0, height: 4, width: `${p * 100}%`, background: color }} />
      )}
    </div>
  );
};

export const Graph: React.FC<{ w: number; h: number; vertical: boolean }> = ({ w, h, vertical }) => {
  const frame = useCurrentFrame();
  const bodyW = w - CH * 3;
  const listW = vertical ? 0 : bodyW * 0.24;
  const GW = bodyW - listW - (vertical ? 0 : CH * 1.5) - CH * 2;
  const s = vertical ? 1.5 : 1;
  const nh = ROW * 2.3;
  const gap = CH * 1.4;
  const nw = Math.min(CH * 22, (GW - gap * 5) / 4);
  const cx = GW / 2;
  const laneX = LANES.map((_, i) => cx + (i - 1.5) * (nw + gap));
  const y0 = ROW * 1.2;
  const y1 = y0 + nh + ROW * 1.2 * s;
  const y2 = y1 + nh + ROW * 1.6 * s;
  const y3 = y2 + nh + ROW * 1.0 * s;
  const yJoin = y3 + nh + ROW * 0.7 * s;
  const y4 = yJoin + ROW * 0.7 * s;

  const lit = (id: string) => (stOf(id, frame) === "done" ? T.green : T.border);
  const allLanes = ["api_test", "tui_impl", "docs", "release"].every((id) => stOf(id, frame) === "done");
  const done = stOf("approve", frame) === "done";

  const graph = (
    <div style={{ position: "relative", width: GW, height: y4 + nh + ROW }}>
      <svg width={GW} height={y4 + nh} style={{ position: "absolute", left: 0, top: 0 }}>
        <g strokeWidth={2} fill="none">
          <path d={`M${cx},${y0 + nh} V${y1}`} stroke={lit("plan")} />
          <path d={`M${cx},${y1 + nh} V${y1 + nh + ROW * 0.6 * s} H${laneX[0]} M${cx},${y1 + nh + ROW * 0.6 * s} H${laneX[3]}`} stroke={lit("fan")} />
          {laneX.map((x, i) => (
            <path key={i} d={`M${x},${y1 + nh + ROW * 0.6 * s} V${y2}`} stroke={lit("fan")} />
          ))}
          <path d={`M${laneX[0]},${y2 + nh} V${y3}`} stroke={lit("api_impl")} />
          <path d={`M${laneX[0]},${y3 + nh} V${yJoin}`} stroke={lit("api_test")} />
          {laneX.slice(1).map((x, i) => (
            <path key={i} d={`M${x},${y2 + nh} V${yJoin}`} stroke={lit(LANES[i + 1].id)} />
          ))}
          <path d={`M${laneX[0]},${yJoin} H${laneX[3]} M${cx},${yJoin} V${y4}`} stroke={allLanes ? T.green : T.border} />
        </g>
      </svg>
      <div
        style={{
          position: "absolute",
          left: laneX[0] - nw / 2 - CH,
          width: laneX[3] - laneX[0] + nw + CH * 2,
          top: y1 + nh + ROW * 0.6 * s - ROW * 0.5,
          color: T.dim,
          display: "flex",
          justifyContent: "space-around",
        }}
      >
        {LANES.map((l) => (
          <span key={l.lane} style={{ background: T.bg, padding: `0 ${CH / 2}px` }}>
            {l.lane}
          </span>
        ))}
      </div>
      <Node x={cx} y={y0} w={nw} h={nh} id="plan" name="plan" type="agent · claude" frame={frame} />
      <Node x={cx} y={y1} w={nw} h={nh} id="fan" name="delivery_lanes" type="fan_out" frame={frame} />
      {LANES.map((l, i) => (
        <Node key={l.id} x={laneX[i]} y={y2} w={nw} h={nh} id={l.id} name={l.id.replace("_", "_")} type={l.type} frame={frame} />
      ))}
      <Node x={laneX[0]} y={y3} w={nw} h={nh} id="api_test" name="api_test" type="command" frame={frame} />
      <Node x={cx} y={y4} w={nw} h={nh} id="approve" name="approve" type="manual" frame={frame} />
    </div>
  );

  return (
    <div style={{ position: "relative" }}>
      <Term
        w={w}
        h={h}
        crumb={<span style={{ color: T.dim }}> Tasks › #31 › Workflow</span>}
        footer={
          <Footer
            mode="Task #31"
            keys={GRAPH_KEYS}
            hints={vertical ? [["5", "workflow"], ["a", "approve"], ["l", "lane"]] : [["tab", "tabs"], ["5", "workflow"], ["l", "open the lane"], ["a", "approve"], ["r", "retry"], ["?", "help"]]}
          />
        }
      >
        <div style={{ display: "flex", gap: CH * 1.5, height: "100%" }}>
          {!vertical && (
            <Panel
              title={<span style={{ color: T.cyan }}>⑂ Lanes</span>}
              right={<span style={{ color: T.cyan }}>4</span>}
              style={{ width: listW, whiteSpace: "nowrap", overflow: "hidden" }}
            >
              {LANES.map((l) => {
                const id = l.id === "api_impl" ? "api_test" : l.id;
                const st = stOf(id, frame);
                const [st0] = RUN[l.id];
                const p = interpolate(frame, [st0, RUN[id][1]], [0, 1], clamp);
                const color = st === "done" ? T.green : st === "run" ? T.cyan : T.dim;
                return (
                  <div key={l.id} style={{ marginBottom: ROW * 0.6 }}>
                    <div style={{ display: "flex", justifyContent: "space-between" }}>
                      <b>{l.lane}</b>
                      <span style={{ color }}>{st === "done" ? "✓ merged" : st === "run" ? "running" : "queued"}</span>
                    </div>
                    <div style={{ height: 8, background: T.sel, marginTop: 4 }}>
                      <div style={{ height: 8, width: `${p * 100}%`, background: color }} />
                    </div>
                  </div>
                );
              })}
            </Panel>
          )}
          <Panel
            title={<span style={{ color: T.cyan }}>▦ Graph</span>}
            right={done ? <Pill bg={T.green}>done</Pill> : <span style={{ color: T.dim }}>feature-delivery</span>}
            style={{ flex: 1, overflow: "hidden" }}
          >
            {graph}
          </Panel>
        </div>
      </Term>
      <KeyCast keys={GRAPH_KEYS} />
    </div>
  );
};

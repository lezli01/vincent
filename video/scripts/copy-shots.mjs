// Copies the real TUI captures the 0.14.0 cut's redesign montage and its
// thumbnail show from
// docs/assets/ (written only by scripts/screenshots.sh) into public/shots/,
// so the video never carries a second, drifting copy of them in git.
import { copyFileSync, mkdirSync } from "node:fs";

const SHOTS = [
  "tui-board",
  "tui-issues-preview",
  "tui-task-overview-running",
  "tui-task-output",
  "tui-new-task",
  "tui-project-overview",
  "tui-daemon-adapters",
  "tui-chat-compare",
];

const out = new URL("../public/shots/", import.meta.url);
mkdirSync(out, { recursive: true });
for (const s of SHOTS) copyFileSync(new URL(`../../docs/assets/${s}.png`, import.meta.url), new URL(`${s}.png`, out));
console.log(`public/shots: ${SHOTS.length} captures`);

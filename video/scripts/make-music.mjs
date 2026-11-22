// Synthesizes the promo's soundtrack into public/music.wav — an original
// synthwave loop, so there is no licence to clear. No dependencies.
//
//   node scripts/make-music.mjs [seconds] [dropAt] [outroAt] [outFile]
//
// outFile is a name under public/ (default music.wav), so each cut can carry
// its own length.
//
// 120 BPM: one beat is 0.5 s, i.e. 15 frames at 30 fps, so cuts land on beats.
import { writeFileSync } from "node:fs";

const SR = 44100;
const DUR = Number(process.argv[2] ?? 56.1);
const DROP = Number(process.argv[3] ?? 3.0); // hook → logo reveal
const OUTRO = Number(process.argv[4] ?? 52.1); // repo-link card
const OUT = process.argv[5] ?? "music.wav";
const BPM = 120;
const BEAT = 60 / BPM;
const N = Math.ceil(SR * DUR);
const L = new Float32Array(N);
const R = new Float32Array(N);

const midi = (m) => 440 * 2 ** ((m - 69) / 12);
// Am – F – C – G, two bars each.
const CHORDS = [
  [57, 60, 64],
  [53, 57, 60],
  [48, 52, 55],
  [55, 59, 62],
];
const chordAt = (t) => CHORDS[Math.floor(t / (BEAT * 8)) % 4];

let seed = 1;
const noise = () => ((seed = (seed * 1664525 + 1013904223) >>> 0) / 2 ** 32) * 2 - 1;
const saw = (ph) => 2 * (ph - Math.floor(ph + 0.5));

// Section gains: 0..1 over time.
const ramp = (t, a, b) => Math.min(1, Math.max(0, (t - a) / (b - a)));
const drums = (t) => (t >= DROP && t < OUTRO ? 1 : 0);
const end = (t) => 1 - ramp(t, DUR - 3, DUR); // final fade

// Sidechain: everything melodic ducks under the kick.
const duck = (t) => {
  if (!drums(t)) return 1;
  const since = t % BEAT;
  return 0.45 + 0.55 * Math.min(1, since / 0.22);
};

// ── drums ──────────────────────────────────────────────────────────────────
for (let i = 0; i < N; i++) {
  const t = i / SR;
  const d = drums(t);
  let s = 0;
  if (d) {
    const kb = t % BEAT;
    s += Math.sin(2 * Math.PI * (45 * kb + 90 * (1 - Math.exp(-kb * 30)) / 30)) * Math.exp(-kb * 9) * 0.9;
    const beatN = Math.floor(t / BEAT);
    if (beatN % 2 === 1) s += noise() * Math.exp(-kb * 22) * 0.35; // clap on 2 & 4
    const half = (t + BEAT / 2) % BEAT; // offbeat open hat
    s += noise() * Math.exp(-half * 35) * 0.12;
  }
  // 16th ticks through the hook, building tension
  if (t < DROP) {
    const sx = t % (BEAT / 4);
    s += noise() * Math.exp(-sx * 120) * 0.08 * ramp(t, 0, DROP);
  }
  L[i] += s;
  R[i] += s;
}

// ── riser into the drop ────────────────────────────────────────────────────
{
  let lp = 0;
  for (let i = 0; i < Math.ceil(DROP * SR); i++) {
    const t = i / SR;
    const k = ramp(t, 0, DROP);
    lp += (0.02 + 0.5 * k * k) * (noise() - lp);
    const s = lp * 0.35 * k;
    L[i] += s;
    R[i] += s;
  }
}

// ── bass: chord roots in eighths, filtered saw ─────────────────────────────
{
  let ph = 0;
  let lp = 0;
  for (let i = 0; i < N; i++) {
    const t = i / SR;
    const f = midi(chordAt(t)[0] - 12);
    ph += f / SR;
    const e8 = t % (BEAT / 2);
    const env = t < DROP ? 0.5 : Math.exp(-e8 * 5) * 0.8 + 0.2;
    const cutoff = t < DROP ? 0.02 + 0.03 * ramp(t, 0, DROP) : 0.08;
    lp += cutoff * (saw(ph) - lp);
    const s = lp * env * 0.55 * duck(t) * (t >= OUTRO ? 1 - ramp(t, OUTRO, OUTRO + 1) : 1);
    L[i] += s;
    R[i] += s;
  }
}

// ── pad: detuned saws on the chord, slow swell ─────────────────────────────
{
  const ph = new Float64Array(6);
  let lpL = 0;
  let lpR = 0;
  for (let i = 0; i < N; i++) {
    const t = i / SR;
    const ch = chordAt(t);
    let a = 0;
    let b = 0;
    for (let v = 0; v < 6; v++) {
      const f = midi(ch[v % 3]) * (v < 3 ? 1.004 : 0.996);
      ph[v] += f / SR;
      if (v < 3) a += saw(ph[v]);
      else b += saw(ph[v]);
    }
    const open = t < DROP ? 0.015 : 0.035;
    lpL += open * (a - lpL);
    lpR += open * (b - lpR);
    const g = 0.12 * (0.4 + 0.6 * ramp(t, 0, DROP)) * duck(t);
    L[i] += lpL * g;
    R[i] += lpR * g;
  }
}

// ── arp: 16th notes over chord tones, with a ping-pong delay ───────────────
{
  const arpL = new Float32Array(N);
  const arpR = new Float32Array(N);
  let ph = 0;
  let lp = 0;
  const pattern = [0, 1, 2, 1, 2, 0, 1, 2];
  for (let i = 0; i < N; i++) {
    const t = i / SR;
    if (t < DROP - BEAT * 2) continue;
    const step = Math.floor(t / (BEAT / 4));
    const ch = chordAt(t);
    const note = ch[pattern[step % 8]] + 12 + (step % 16 >= 8 ? 12 : 0);
    ph += midi(note) / SR;
    const e = Math.exp(-(t % (BEAT / 4)) * 18);
    lp += 0.18 * (saw(ph) + 0.5 * Math.sign(Math.sin(2 * Math.PI * ph)) - lp);
    const g = 0.16 * e * ramp(t, DROP - BEAT * 2, DROP) * duck(t);
    arpL[i] = lp * g;
    arpR[i] = lp * g;
  }
  const dly = Math.round(SR * BEAT * 0.75);
  for (let i = dly; i < N; i++) {
    arpL[i] += arpR[i - dly] * 0.45;
    arpR[i] += arpL[i - dly] * 0.45;
  }
  for (let i = 0; i < N; i++) {
    L[i] += arpL[i];
    R[i] += arpR[i];
  }
}

// ── master: fade, soft clip, normalise, 16-bit WAV ─────────────────────────
let peak = 0;
for (let i = 0; i < N; i++) {
  const g = end(i / SR);
  L[i] = Math.tanh(L[i] * 1.2) * g;
  R[i] = Math.tanh(R[i] * 1.2) * g;
  peak = Math.max(peak, Math.abs(L[i]), Math.abs(R[i]));
}
const norm = 0.89 / peak;
const buf = Buffer.alloc(44 + N * 4);
buf.write("RIFF", 0);
buf.writeUInt32LE(36 + N * 4, 4);
buf.write("WAVEfmt ", 8);
buf.writeUInt32LE(16, 16);
buf.writeUInt16LE(1, 20);
buf.writeUInt16LE(2, 22);
buf.writeUInt32LE(SR, 24);
buf.writeUInt32LE(SR * 4, 28);
buf.writeUInt16LE(4, 32);
buf.writeUInt16LE(16, 34);
buf.write("data", 36);
buf.writeUInt32LE(N * 4, 40);
for (let i = 0; i < N; i++) {
  buf.writeInt16LE(Math.round(L[i] * norm * 32767), 44 + i * 4);
  buf.writeInt16LE(Math.round(R[i] * norm * 32767), 46 + i * 4);
}
writeFileSync(new URL(`../public/${OUT}`, import.meta.url), buf);
console.log(`${OUT}: ${DUR}s, drop at ${DROP}s, outro at ${OUTRO}s`);

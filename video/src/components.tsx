import React from "react";
import {
  AbsoluteFill,
  interpolate,
  spring,
  useCurrentFrame,
  useVideoConfig,
} from "remotion";
import { C, mono, sans, useIsVertical } from "./theme";

/** Fades a scene in and out at its edges so hard cuts still feel snappy. */
export const SceneFade: React.FC<{ children: React.ReactNode; out?: number }> = ({ children, out = 8 }) => {
  const frame = useCurrentFrame();
  const { durationInFrames } = useVideoConfig();
  const opacity = interpolate(frame, [0, 6, durationInFrames - out, durationInFrames], [0, 1, 1, 0], {
    extrapolateLeft: "clamp",
    extrapolateRight: "clamp",
  });
  return <AbsoluteFill style={{ opacity }}>{children}</AbsoluteFill>;
};

/** A slow drifting glow behind everything. */
export const Backdrop: React.FC = () => {
  const frame = useCurrentFrame();
  const x = 50 + Math.sin(frame / 90) * 18;
  const y = 40 + Math.cos(frame / 110) * 14;
  return (
    <AbsoluteFill
      style={{
        background: `radial-gradient(circle at ${x}% ${y}%, rgba(34,195,214,0.20), transparent 55%),
                     radial-gradient(circle at ${100 - x}% ${100 - y}%, rgba(180,140,255,0.12), transparent 50%), ${C.bg}`,
      }}
    >
      <AbsoluteFill
        style={{
          backgroundImage:
            "linear-gradient(rgba(255,255,255,0.035) 1px, transparent 1px), linear-gradient(90deg, rgba(255,255,255,0.035) 1px, transparent 1px)",
          backgroundSize: "48px 48px",
          maskImage: "radial-gradient(circle at 50% 50%, black, transparent 75%)",
        }}
      />
    </AbsoluteFill>
  );
};

/**
 * Kinetic headline. Words wrapped in *asterisks* are painted in the accent
 * colour. Each word springs up in turn.
 */
export const Headline: React.FC<{
  text: string;
  size?: number;
  accent?: string;
  delay?: number;
  align?: "center" | "left";
}> = ({ text, size, accent = C.teal, delay = 0, align = "center" }) => {
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  const vertical = useIsVertical();
  const fontSize = size ?? (vertical ? 92 : 84);
  const words = text.split(" ");
  return (
    <div
      style={{
        fontFamily: sans,
        fontWeight: 900,
        fontSize,
        lineHeight: 1.05,
        letterSpacing: -fontSize * 0.03,
        color: C.text,
        textAlign: align,
        display: "flex",
        flexWrap: "wrap",
        justifyContent: align === "center" ? "center" : "flex-start",
        gap: `0 ${fontSize * 0.26}px`,
      }}
    >
      {words.map((w, i) => {
        const hot = w.startsWith("*");
        const clean = w.replace(/\*/g, "");
        const s = spring({ frame: frame - delay - i * 3, fps, config: { damping: 14, mass: 0.6 } });
        return (
          <span
            key={i}
            style={{
              display: "inline-block",
              transform: `translateY(${(1 - s) * 40}px) scale(${0.9 + s * 0.1})`,
              opacity: s,
              color: hot ? accent : C.text,
              textShadow: hot ? `0 0 40px ${accent}88` : "none",
            }}
          >
            {clean}
          </span>
        );
      })}
    </div>
  );
};

export const Kicker: React.FC<{ children: React.ReactNode; color?: string; delay?: number }> = ({
  children,
  color = C.teal,
  delay = 0,
}) => {
  const frame = useCurrentFrame();
  const vertical = useIsVertical();
  const o = interpolate(frame - delay, [0, 8], [0, 1], { extrapolateLeft: "clamp", extrapolateRight: "clamp" });
  return (
    <div
      style={{
        fontFamily: mono,
        fontWeight: 700,
        fontSize: vertical ? 34 : 28,
        color,
        letterSpacing: 2,
        textTransform: "uppercase",
        opacity: o,
        transform: `translateX(${(1 - o) * -20}px)`,
      }}
    >
      {children}
    </div>
  );
};

export const Chip: React.FC<{ label: string; color: string; delay: number; big?: boolean }> = ({
  label,
  color,
  delay,
  big,
}) => {
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  const s = spring({ frame: frame - delay, fps, config: { damping: 11, mass: 0.7 } });
  return (
    <div
      style={{
        fontFamily: mono,
        fontWeight: 700,
        fontSize: big ? 44 : 32,
        color,
        padding: big ? "18px 34px" : "12px 24px",
        borderRadius: 999,
        border: `3px solid ${color}`,
        background: `${color}1a`,
        boxShadow: `0 0 30px ${color}44`,
        transform: `scale(${s})`,
        opacity: Math.min(1, s * 1.5),
        whiteSpace: "nowrap",
      }}
    >
      {label}
    </div>
  );
};

/** Thin progress bar along the top edge — keeps people watching to the end. */
export const Progress: React.FC = () => {
  const frame = useCurrentFrame();
  const { durationInFrames, width } = useVideoConfig();
  return (
    <div
      style={{
        position: "absolute",
        top: 0,
        left: 0,
        height: 8,
        width: (frame / durationInFrames) * width,
        background: `linear-gradient(90deg, ${C.teal}, ${C.violet})`,
        boxShadow: `0 0 20px ${C.teal}`,
      }}
    />
  );
};

import { loadFont as loadInter } from "@remotion/google-fonts/Inter";
import { loadFont as loadMono } from "@remotion/google-fonts/JetBrainsMono";
import { useVideoConfig } from "remotion";

export const sans = loadInter("normal", { weights: ["500", "800", "900"], subsets: ["latin"] }).fontFamily;
export const mono = loadMono("normal", { weights: ["400", "700"], subsets: ["latin"] }).fontFamily;

// Pulled from the logo and the TUI's own palette.
export const C = {
  bg: "#07090f",
  panel: "#141824",
  border: "#2a3142",
  text: "#e8edf5",
  dim: "#7d879a",
  teal: "#22c3d6",
  amber: "#f5b84d",
  green: "#4fd18b",
  red: "#ff5f6d",
  violet: "#b48cff",
  blue: "#7aa2f7",
};

export const useIsVertical = () => {
  const { width, height } = useVideoConfig();
  return height > width;
};

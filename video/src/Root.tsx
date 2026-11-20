import { Composition } from "remotion";
import { Promo, TOTAL_FRAMES, FPS } from "./Promo";

// Two cuts of the same edit: 9:16 for Reels / Shorts / TikTok, 16:9 for X,
// LinkedIn and YouTube. Every scene reads the orientation from
// useVideoConfig, so there is one timeline to keep in sync.
export const Root = () => (
  <>
    <Composition
      id="VincentVertical"
      component={Promo}
      durationInFrames={TOTAL_FRAMES}
      fps={FPS}
      width={1080}
      height={1920}
    />
    <Composition
      id="VincentLandscape"
      component={Promo}
      durationInFrames={TOTAL_FRAMES}
      fps={FPS}
      width={1920}
      height={1080}
    />
  </>
);

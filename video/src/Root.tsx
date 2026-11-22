import { Composition, Still } from "remotion";
import { Promo, TOTAL_FRAMES, FPS } from "./Promo";
import { Release014, TOTAL_FRAMES_014 } from "./release014/Release014";
import { Thumbnail } from "./release014/Thumbnail";

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
    {/* The 0.14.0 release cut, in the same two shapes. */}
    <Composition id="Release014Vertical" component={Release014} durationInFrames={TOTAL_FRAMES_014} fps={FPS} width={1080} height={1920} />
    <Composition id="Release014Landscape" component={Release014} durationInFrames={TOTAL_FRAMES_014} fps={FPS} width={1920} height={1080} />
    <Still id="Release014Thumbnail" component={Thumbnail} width={1280} height={720} />
  </>
);

import { useState, useEffect, useRef } from "react";
import { cn } from "@/lib/utils";
import { ScrambleText } from "@/components/ui/scramble-text";

/**
 * Whimsical "working" verbs, in the spirit of Claude Code's loading messages.
 * Cycled through in order from a random starting offset (see below).
 */
const VERBS = [
  "Thinking",
  "Pondering",
  "Cogitating",
  "Ruminating",
  "Musing",
  "Conjuring",
  "Noodling",
  "Percolating",
  "Marinating",
  "Brewing",
  "Synthesizing",
  "Untangling",
  "Divining",
  "Scheming",
  "Concocting",
  "Deliberating",
  "Contemplating",
  "Wrangling",
  "Mulling",
  "Moonwalking",
  "Reticulating",
  "Spelunking",
  "Tinkering",
  "Puzzling",
  "Theorizing",
  "Brainstorming",
  "Daydreaming",
  "Hatching",
  "Incubating",
  "Distilling",
  "Crystallizing",
  "Pontificating",
  "Philosophizing",
  "Manifesting",
  "Channeling",
  "Summoning",
  "Finagling",
  "Simmering",
  "Whisking",
  "Doodling",
  "Riffing",
  "Plotting",
  "Strategizing",
  "Architecting",
  "Decoding",
  "Unraveling",
  "Crunching",
  "Juggling",
  "Orchestrating",
  "Composing",
  "Calculating",
  "Computing",
  "Processing",
  "Buffering",
  "Imagining",
  "Dreaming",
  "Wondering",
  "Reckoning",
  "Speculating",
  "Hypothesizing",
  "Brooding",
  "Meditating",
  "Reflecting",
  "Ideating",
  "Germinating",
  "Bamboozling",
  "Cooking",
  "Stewing",
  "Steeping",
  "Fermenting",
  "Sculpting",
  "Weaving",
  "Forging",
  "Tempering",
  "Polishing",
  "Refining",
  "Calibrating",
  "Tuning",
  "Mapping",
  "Charting",
  "Surveying",
  "Excavating",
  "Sifting",
  "Parsing",
  "Compiling",
  "Assembling",
  "Bootstrapping",
  "Marshalling",
  "Herding",
  "Disentangling",
  "Deciphering",
  "Extrapolating",
  "Deducing",
  "Conjecturing",
  "Formulating",
  "Devising",
  "Fabricating",
  "Crafting",
  "Prototyping",
  "Optimizing",
];

/**
 * Initial "the model is thinking" indicator: a mood-light spinner followed by a
 * whimsical verb that swaps every 5s. To avoid the clustering/repeats of pure
 * random choice, we pick a random starting offset and then walk the pool in
 * order — so all of the verbs cycle through before any repeats. The verb
 * "decodes" into place via ScrambleText, and its color drifts through a pastel
 * hue cycle (see `.mood-text` in index.css). font-mono keeps the width stable
 * while characters scramble. The "working" motion now lives in the BorderBeam
 * at the bottom of the streaming card, so this indicator carries no spinner.
 */
export function ThinkingIndicator({ className }: { className?: string }) {
  const indexRef = useRef(Math.floor(Math.random() * VERBS.length));
  const [verb, setVerb] = useState(() => VERBS[indexRef.current]);

  useEffect(() => {
    const id = setInterval(() => {
      indexRef.current = (indexRef.current + 1) % VERBS.length;
      setVerb(VERBS[indexRef.current]);
    }, 5000);
    return () => clearInterval(id);
  }, []);

  return (
    <div className={cn("flex items-center", className)}>
      <span className="mood-text text-sm font-mono">
        <ScrambleText text={verb} />…
      </span>
    </div>
  );
}

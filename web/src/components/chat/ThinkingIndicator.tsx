import { useState, useEffect } from "react";
import { cn } from "@/lib/utils";
import { Spinner } from "@/components/ui/spinner";
import { ScrambleText } from "@/components/ui/scramble-text";

/**
 * Whimsical "working" verbs, in the spirit of Claude Code's loading messages.
 * One is picked at random and swapped every few seconds while the model works.
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
];

function randomVerb(exclude?: string): string {
  let verb = exclude;
  while (verb === exclude) verb = VERBS[Math.floor(Math.random() * VERBS.length)];
  return verb as string;
}

/**
 * Initial "the model is thinking" indicator: a mood-light spinner followed by a
 * verb that swaps every 5s, picked at random from a pool of whimsical gerunds
 * (the first word is random too — "Thinking" is just one of the pool). The verb
 * "decodes" into place via ScrambleText, and its color drifts through the same
 * pastel hue cycle as the spinner (see `.mood-text` in index.css). font-mono
 * keeps the width stable while characters scramble.
 */
export function ThinkingIndicator({ className }: { className?: string }) {
  const [verb, setVerb] = useState(() => randomVerb());

  useEffect(() => {
    const id = setInterval(() => setVerb((cur) => randomVerb(cur)), 5000);
    return () => clearInterval(id);
  }, []);

  return (
    <div className={cn("flex items-center gap-2", className)}>
      <Spinner className="size-4" />
      <span className="mood-text text-sm font-mono">
        <ScrambleText text={verb} />…
      </span>
    </div>
  );
}

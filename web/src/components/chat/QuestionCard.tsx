import { useState, useCallback } from "react";
import { CircleHelp, ChevronUp, ChevronDown, Check } from "lucide-react";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";

// Types matching the MCP tool schema
interface QuestionOption {
  id: string;
  label: string;
}

interface Question {
  id: string;
  prompt: string;
  options: QuestionOption[];
  allow_multiple?: boolean;
}

export interface QuestionAnswer {
  question_id: string;
  selected_options: string[];
  // Free-text answer typed when the synthetic "Other" option is selected.
  custom_text?: string;
}

interface QuestionCardProps {
  title?: string;
  questions: Question[];
  onSubmit: (answers: QuestionAnswer[]) => void;
  onSkip: () => void;
  disabled?: boolean;
}

const OPTION_LETTERS = "ABCDEFGHIJKLMNOPQRSTUVWXYZ";

// Synthetic option id appended to every question so the user can supply an
// answer outside the offered list. Its value comes from a free-text field
// rather than the option label.
export const OTHER_OPTION_ID = "__other__";

export function QuestionCard({ title, questions, onSubmit, onSkip, disabled }: QuestionCardProps) {
  const [currentIndex, setCurrentIndex] = useState(0);
  // selections: map of question_id -> set of selected option_ids
  const [selections, setSelections] = useState<Record<string, Set<string>>>(() => {
    const init: Record<string, Set<string>> = {};
    for (const q of questions) {
      init[q.id] = new Set();
    }
    return init;
  });
  // Free-text typed for the "Other" option, keyed by question_id.
  const [otherText, setOtherText] = useState<Record<string, string>>({});

  const currentQuestion = questions[currentIndex];
  const isMulti = currentQuestion?.allow_multiple ?? false;
  const currentSelection = selections[currentQuestion?.id] ?? new Set();
  const isLastQuestion = currentIndex === questions.length - 1;

  // A question is answered when it has a selection, and — if "Other" is among
  // the selections — its free-text field is non-empty.
  const isAnswered = useCallback(
    (q: Question) => {
      const sel = selections[q.id];
      if (!sel || sel.size === 0) return false;
      if (sel.has(OTHER_OPTION_ID) && !(otherText[q.id] ?? "").trim()) return false;
      return true;
    },
    [selections, otherText]
  );

  const hasSelection = currentQuestion ? isAnswered(currentQuestion) : false;
  // Check if all questions have been answered
  const allAnswered = questions.every((q) => isAnswered(q));
  // Count how many questions have answers
  const answeredCount = questions.filter((q) => isAnswered(q)).length;

  const toggleOption = useCallback(
    (optionId: string) => {
      if (!currentQuestion) return;
      setSelections((prev) => {
        const current = new Set(prev[currentQuestion.id]);
        if (isMulti) {
          if (current.has(optionId)) {
            current.delete(optionId);
          } else {
            current.add(optionId);
          }
        } else {
          current.clear();
          current.add(optionId);
        }
        return { ...prev, [currentQuestion.id]: current };
      });
    },
    [currentQuestion, isMulti]
  );

  const handleContinue = () => {
    if (isLastQuestion) {
      // Submit all answers
      const answers: QuestionAnswer[] = questions.map((q) => {
        const selected = Array.from(selections[q.id] || []);
        const answer: QuestionAnswer = { question_id: q.id, selected_options: selected };
        if (selected.includes(OTHER_OPTION_ID)) {
          answer.custom_text = (otherText[q.id] ?? "").trim();
        }
        return answer;
      });
      onSubmit(answers);
    } else {
      setCurrentIndex((i) => i + 1);
    }
  };

  if (!currentQuestion) return null;

  return (
    <div className="border border-border rounded-lg overflow-hidden bg-background">
      {/* Header */}
      <div className="flex items-center gap-2 px-4 py-2 border-b bg-muted/30">
        <CircleHelp className="h-4 w-4 text-muted-foreground" />
        <span className="text-sm font-medium">{title || "Questions"}</span>

        {/* Pagination */}
        {questions.length > 1 && (
          <div className="ml-auto flex items-center gap-1">
            <button
              onClick={() => setCurrentIndex((i) => Math.max(0, i - 1))}
              disabled={currentIndex === 0}
              className="p-0.5 rounded hover:bg-accent disabled:opacity-30 disabled:cursor-not-allowed"
            >
              <ChevronUp className="h-3.5 w-3.5 text-muted-foreground" />
            </button>
            <span className="text-xs text-muted-foreground tabular-nums">
              {currentIndex + 1} of {questions.length}
            </span>
            <button
              onClick={() => setCurrentIndex((i) => Math.min(questions.length - 1, i + 1))}
              disabled={currentIndex === questions.length - 1}
              className="p-0.5 rounded hover:bg-accent disabled:opacity-30 disabled:cursor-not-allowed"
            >
              <ChevronDown className="h-3.5 w-3.5 text-muted-foreground" />
            </button>
          </div>
        )}
      </div>

      {/* Current question */}
      <div className="px-4 py-3">
        <p className="text-sm font-medium mb-3">
          <span className="text-muted-foreground mr-1.5">{currentIndex + 1}.</span>
          {currentQuestion.prompt}
          {isMulti && <span className="text-muted-foreground font-normal ml-1"> (Multi-select)</span>}
        </p>
        <div className="space-y-1">
          {currentQuestion.options.map((opt, oi) => {
            const isSelected = currentSelection.has(opt.id);
            const letter = OPTION_LETTERS[oi] || String(oi + 1);
            return (
              <button
                key={opt.id}
                onClick={() => !disabled && toggleOption(opt.id)}
                disabled={disabled}
                className={cn(
                  "flex items-center gap-2.5 w-full text-left px-3 py-2 rounded-md text-sm transition-all",
                  isSelected
                    ? "bg-blue-500/15 text-foreground ring-1 ring-blue-500/40"
                    : "hover:bg-accent/50 text-muted-foreground",
                  disabled && "opacity-50 cursor-not-allowed"
                )}
              >
                {/* Letter badge or check */}
                <span
                  className={cn(
                    "flex items-center justify-center w-5 h-5 rounded text-xs font-semibold shrink-0 transition-colors",
                    isSelected
                      ? "bg-blue-500 text-white"
                      : "bg-muted text-muted-foreground"
                  )}
                >
                  {isSelected ? <Check className="h-3 w-3" /> : letter}
                </span>
                <span>{opt.label}</span>
              </button>
            );
          })}

          {/* "Other" — free-text answer outside the offered options */}
          {(() => {
            const isSelected = currentSelection.has(OTHER_OPTION_ID);
            const letter = OPTION_LETTERS[currentQuestion.options.length] || "#";
            return (
              <button
                onClick={() => !disabled && toggleOption(OTHER_OPTION_ID)}
                disabled={disabled}
                className={cn(
                  "flex items-center gap-2.5 w-full text-left px-3 py-2 rounded-md text-sm transition-all",
                  isSelected
                    ? "bg-blue-500/15 text-foreground ring-1 ring-blue-500/40"
                    : "hover:bg-accent/50 text-muted-foreground",
                  disabled && "opacity-50 cursor-not-allowed"
                )}
              >
                <span
                  className={cn(
                    "flex items-center justify-center w-5 h-5 rounded text-xs font-semibold shrink-0 transition-colors",
                    isSelected ? "bg-blue-500 text-white" : "bg-muted text-muted-foreground"
                  )}
                >
                  {isSelected ? <Check className="h-3 w-3" /> : letter}
                </span>
                <span>Other…</span>
              </button>
            );
          })()}

          {currentSelection.has(OTHER_OPTION_ID) && (
            <Textarea
              autoFocus
              value={otherText[currentQuestion.id] ?? ""}
              onChange={(e) =>
                setOtherText((prev) => ({ ...prev, [currentQuestion.id]: e.target.value }))
              }
              onKeyDown={(e) => {
                if (e.nativeEvent.isComposing) return;
                if (e.key === "Enter" && !e.shiftKey) {
                  e.preventDefault();
                  if (!disabled && hasSelection && (!isLastQuestion || allAnswered)) handleContinue();
                }
              }}
              disabled={disabled}
              placeholder="Type your answer…"
              className="mt-1 min-h-[36px] max-h-[120px] resize-none text-sm"
              rows={1}
            />
          )}
        </div>
      </div>

      {/* Footer: Skip / Continue */}
      <div className="flex items-center justify-between px-4 py-2 border-t">
        {/* Progress indicator */}
        <span className="text-xs text-muted-foreground">
          {answeredCount}/{questions.length} answered
        </span>
        <div className="flex items-center gap-2">
          <Button variant="ghost" size="sm" onClick={onSkip} disabled={disabled} className="text-xs">
            Skip
          </Button>
          <Button
            size="sm"
            onClick={handleContinue}
            disabled={disabled || !hasSelection || (isLastQuestion && !allAnswered)}
            className="text-xs"
          >
            {isLastQuestion ? "Submit" : "Next"}
          </Button>
        </div>
      </div>
    </div>
  );
}

// Helper to parse ask_question input from tool call
export function parseAskQuestionInput(input: string): {
  title?: string;
  questions: Question[];
} | null {
  try {
    const parsed = JSON.parse(input);
    const title = parsed.title as string | undefined;
    let questions: Question[];

    if (typeof parsed.questions === "string") {
      questions = JSON.parse(parsed.questions);
    } else if (Array.isArray(parsed.questions)) {
      questions = parsed.questions;
    } else {
      return null;
    }

    // Validate minimum structure
    if (!Array.isArray(questions) || questions.length === 0) return null;
    for (const q of questions) {
      if (!q.id || !q.prompt || !Array.isArray(q.options) || q.options.length < 2) {
        return null;
      }
    }

    return { title, questions };
  } catch {
    return null;
  }
}

// Format answers into a readable message for Claude
export function formatAnswersMessage(
  questions: Question[],
  answers: QuestionAnswer[]
): string {
  const lines: string[] = [];

  for (let i = 0; i < answers.length; i++) {
    const ans = answers[i];
    const q = questions.find((qq) => qq.id === ans.question_id);
    if (!q) continue;

    const selectedLabels = ans.selected_options
      .map((optId) => {
        if (optId === OTHER_OPTION_ID) return ans.custom_text || "Other";
        const opt = q.options.find((o) => o.id === optId);
        return opt ? opt.label : optId;
      })
      .join(", ");

    lines.push(`**Q${i + 1}:** ${q.prompt.replace(/\n/g, " ")}`);
    lines.push("");
    lines.push(`> ${selectedLabels}`);
    lines.push("");
  }

  return lines.join("\n");
}

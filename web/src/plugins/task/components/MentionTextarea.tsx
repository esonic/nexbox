import { useRef } from "react";

interface MentionTextareaProps {
  value: string;
  onChange: (value: string) => void;
  placeholder?: string;
  className?: string;
  minRows?: number;
  onKeyDown?: (e: React.KeyboardEvent<HTMLTextAreaElement>) => void;
}

/**
 * Plain textarea. The original implementation supported @-mention autocomplete
 * via the namespace members API; that API was removed when cloud mode was
 * dropped, so this component now just renders a basic textarea with the same
 * props shape so the rest of the UI keeps working.
 */
export function MentionTextarea({ value, onChange, placeholder, className, minRows = 3, onKeyDown }: MentionTextareaProps) {
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  return (
    <textarea
      ref={textareaRef}
      value={value}
      onChange={(e) => onChange(e.target.value)}
      onKeyDown={onKeyDown}
      placeholder={placeholder}
      rows={minRows}
      className={className}
      spellCheck={false}
    />
  );
}

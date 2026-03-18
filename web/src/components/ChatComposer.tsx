import { type KeyboardEvent, type ReactNode, useEffect, useRef } from "react";

type ChatComposerProps = {
  value: string;
  onChange: (value: string) => void;
  onSubmit: () => void;
  disabled?: boolean;
  placeholder: string;
  className: string;
  textareaClassName: string;
  sendButtonClassName: string;
  sendLabel: string;
  leadingSlot?: ReactNode;
  maxHeight?: number;
};

export function ChatComposer({
  value,
  onChange,
  onSubmit,
  disabled = false,
  placeholder,
  className,
  textareaClassName,
  sendButtonClassName,
  sendLabel,
  leadingSlot,
  maxHeight = 140,
}: ChatComposerProps) {
  const textareaRef = useRef<HTMLTextAreaElement | null>(null);

  useEffect(() => {
    const el = textareaRef.current;
    if (!el) return;
    el.style.height = "auto";
    const nextHeight = Math.min(maxHeight, Math.max(44, el.scrollHeight));
    el.style.height = `${nextHeight}px`;
    el.style.overflowY = el.scrollHeight > maxHeight ? "auto" : "hidden";
  }, [value, maxHeight]);

  const handleKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.nativeEvent.isComposing) return;
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      if (!disabled && value.trim()) onSubmit();
    }
  };

  return (
    <div className={`${className}${leadingSlot ? " has-leading-slot" : ""}`}>
      {leadingSlot ? <div className="chat-composer-leading">{leadingSlot}</div> : null}
      <textarea
        ref={textareaRef}
        className={textareaClassName}
        rows={1}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        onKeyDown={handleKeyDown}
        placeholder={placeholder}
        disabled={disabled}
      />
      <button
        className={sendButtonClassName}
        onClick={onSubmit}
        disabled={disabled || !value.trim()}
        type="button"
      >
        {sendLabel}
      </button>
    </div>
  );
}

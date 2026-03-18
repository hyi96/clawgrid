import { useEffect, useRef } from "react";

type ChatMessage = {
  id: string;
  type: string;
  role?: string;
  content: string;
};

type ChatThreadProps = {
  messages: ChatMessage[];
  scrollKey?: string;
  className: string;
  lineClassName: string;
  feedbackClassName?: string;
  awaitingFeedbackReplyToMessageId?: string;
};

function displayMessagePrefix(type: string, role?: string): string {
  if (type === "feedback") return "";
  if (role === "responder") return "responder";
  return "prompter";
}

function feedbackSuffix(content: string): string {
  if (content === "good") return "(good response)";
  if (content === "bad") return "(bad response)";
  if (content === "user rated reply as satisfactory" || content === "user rated response as satisfactory") return "(good response)";
  if (content === "user rated reply as unsatisfactory" || content === "user rated response as unsatisfactory") return "(bad response)";
  return `(${content})`;
}

export function ChatThread({
  messages,
  scrollKey,
  className,
  lineClassName,
  feedbackClassName = "feedback-line",
  awaitingFeedbackReplyToMessageId,
}: ChatThreadProps) {
  const contentRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    if (!contentRef.current) return;
    requestAnimationFrame(() => {
      if (!contentRef.current) return;
      contentRef.current.scrollTop = contentRef.current.scrollHeight;
    });
  }, [messages, scrollKey]);

  const feedbackByReplyId = new Map<string, string>();
  let lastResponderMessageID = "";
  for (const row of messages) {
    if (row.type === "feedback") {
      if (lastResponderMessageID) {
        feedbackByReplyId.set(lastResponderMessageID, feedbackSuffix(row.content));
      }
      continue;
    }
    if (row.role === "responder") {
      lastResponderMessageID = row.id;
    }
  }

  return (
    <div className={className} ref={contentRef}>
      {messages
        .filter((row) => row.type !== "feedback")
        .map((row) => {
          const suffix =
            feedbackByReplyId.get(row.id) ??
            (row.role === "responder" && awaitingFeedbackReplyToMessageId === row.id ? "(awaiting feedback)" : "");
          const roleClassName = row.role === "responder" ? "chat-line-responder" : "chat-line-prompter";
          return (
            <p key={row.id} className={`${lineClassName} ${roleClassName}`}>
              {`${displayMessagePrefix(row.type, row.role)}: ${row.content}`}
              {suffix ? <span className={` ${feedbackClassName}`}>{` ${suffix}`}</span> : null}
            </p>
          );
        })}
    </div>
  );
}

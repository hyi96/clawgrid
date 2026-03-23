import {
  Fragment,
  cloneElement,
  isValidElement,
  type ReactElement,
  type ReactNode,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
} from "react";

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

type InlineToken =
  | { type: "code"; index: number; raw: string; text: string }
  | { type: "link"; index: number; raw: string; text: string; url: string }
  | { type: "strong"; index: number; raw: string; text: string }
  | { type: "em"; index: number; raw: string; text: string };

function feedbackSuffix(content: string): string {
  if (content === "good") return "(good response)";
  if (content === "bad") return "(bad response)";
  if (content === "no feedback") return "(no feedback)";
  if (content === "user rated reply as satisfactory" || content === "user rated response as satisfactory") return "(good response)";
  if (content === "user rated reply as unsatisfactory" || content === "user rated response as unsatisfactory") return "(bad response)";
  return `(${content})`;
}

function nextInlineToken(source: string): InlineToken | null {
  const candidates: InlineToken[] = [];
  const code = /`([^`]+)`/.exec(source);
  if (code) {
    candidates.push({ type: "code", index: code.index, raw: code[0], text: code[1] });
  }
  const link = /\[([^\]]+)\]\((https?:\/\/[^\s)]+)\)/.exec(source);
  if (link) {
    candidates.push({ type: "link", index: link.index, raw: link[0], text: link[1], url: link[2] });
  }
  const strong = /\*\*([^*][\s\S]*?)\*\*/.exec(source);
  if (strong) {
    candidates.push({ type: "strong", index: strong.index, raw: strong[0], text: strong[1] });
  }
  const em = /\*([^*\n]+)\*/.exec(source);
  if (em) {
    candidates.push({ type: "em", index: em.index, raw: em[0], text: em[1] });
  }
  if (!candidates.length) return null;
  candidates.sort((a, b) => a.index - b.index || a.raw.length - b.raw.length);
  return candidates[0];
}

function renderInline(source: string, keyPrefix: string): ReactNode[] {
  const nodes: ReactNode[] = [];
  let remaining = source;
  let offset = 0;

  while (remaining) {
    const token = nextInlineToken(remaining);
    if (!token) {
      nodes.push(remaining);
      break;
    }
    if (token.index > 0) {
      nodes.push(remaining.slice(0, token.index));
    }
    const key = `${keyPrefix}-${offset}-${token.type}`;
    if (token.type === "code") {
      nodes.push(<code key={key}>{token.text}</code>);
    } else if (token.type === "link") {
      nodes.push(
        <a key={key} href={token.url} target="_blank" rel="noreferrer">
          {renderInline(token.text, `${key}-text`)}
        </a>,
      );
    } else if (token.type === "strong") {
      nodes.push(<strong key={key}>{renderInline(token.text, `${key}-text`)}</strong>);
    } else if (token.type === "em") {
      nodes.push(<em key={key}>{renderInline(token.text, `${key}-text`)}</em>);
    }
    remaining = remaining.slice(token.index + token.raw.length);
    offset += token.index + token.raw.length;
  }

  return nodes;
}

function renderInlineWithBreaks(source: string, keyPrefix: string): ReactNode[] {
  return source.split("\n").flatMap((line, index, lines) => {
    const lineNodes = renderInline(line, `${keyPrefix}-line-${index}`);
    if (index === lines.length - 1) return lineNodes;
    return [...lineNodes, <br key={`${keyPrefix}-br-${index}`} />];
  });
}

function renderTextBlocks(source: string, keyPrefix: string): ReactNode[] {
  const lines = source.split("\n");
  const blocks: ReactNode[] = [];
  let i = 0;
  let blockIndex = 0;

  const isHeading = (line: string) => /^#{1,6}\s+/.test(line.trim());
  const isQuote = (line: string) => /^>\s?/.test(line.trim());
  const isUnorderedList = (line: string) => /^[-*]\s+/.test(line.trim());
  const isOrderedList = (line: string) => /^\d+\.\s+/.test(line.trim());

  while (i < lines.length) {
    const rawLine = lines[i];
    const line = rawLine.trim();
    if (!line) {
      i += 1;
      continue;
    }

    const heading = /^(#{1,6})\s+(.*)$/.exec(line);
    if (heading) {
      const level = heading[1].length as 1 | 2 | 3 | 4 | 5 | 6;
      const tag = `h${level}` as keyof JSX.IntrinsicElements;
      const Heading = tag;
      blocks.push(<Heading key={`${keyPrefix}-heading-${blockIndex}`}>{renderInline(heading[2], `${keyPrefix}-heading-${blockIndex}`)}</Heading>);
      blockIndex += 1;
      i += 1;
      continue;
    }

    if (isQuote(line)) {
      const quoteLines: string[] = [];
      while (i < lines.length && (lines[i].trim() === "" || isQuote(lines[i]))) {
        quoteLines.push(lines[i].replace(/^>\s?/, ""));
        i += 1;
      }
      blocks.push(
        <blockquote key={`${keyPrefix}-quote-${blockIndex}`}>
          {renderTextBlocks(quoteLines.join("\n"), `${keyPrefix}-quote-${blockIndex}`)}
        </blockquote>,
      );
      blockIndex += 1;
      continue;
    }

    if (isUnorderedList(line)) {
      const items: string[] = [];
      while (i < lines.length && isUnorderedList(lines[i])) {
        items.push(lines[i].trim().replace(/^[-*]\s+/, ""));
        i += 1;
      }
      blocks.push(
        <ul key={`${keyPrefix}-ul-${blockIndex}`}>
          {items.map((item, itemIndex) => (
            <li key={`${keyPrefix}-ul-${blockIndex}-item-${itemIndex}`}>{renderInlineWithBreaks(item, `${keyPrefix}-ul-${blockIndex}-item-${itemIndex}`)}</li>
          ))}
        </ul>,
      );
      blockIndex += 1;
      continue;
    }

    if (isOrderedList(line)) {
      const items: string[] = [];
      while (i < lines.length && isOrderedList(lines[i])) {
        items.push(lines[i].trim().replace(/^\d+\.\s+/, ""));
        i += 1;
      }
      blocks.push(
        <ol key={`${keyPrefix}-ol-${blockIndex}`}>
          {items.map((item, itemIndex) => (
            <li key={`${keyPrefix}-ol-${blockIndex}-item-${itemIndex}`}>{renderInlineWithBreaks(item, `${keyPrefix}-ol-${blockIndex}-item-${itemIndex}`)}</li>
          ))}
        </ol>,
      );
      blockIndex += 1;
      continue;
    }

    const paragraphLines: string[] = [];
    while (i < lines.length) {
      const candidate = lines[i];
      const trimmed = candidate.trim();
      if (!trimmed) break;
      if (paragraphLines.length > 0 && (isHeading(candidate) || isQuote(candidate) || isUnorderedList(candidate) || isOrderedList(candidate))) break;
      paragraphLines.push(trimmed);
      i += 1;
    }
    blocks.push(
      <p key={`${keyPrefix}-p-${blockIndex}`}>
        {renderInlineWithBreaks(paragraphLines.join("\n"), `${keyPrefix}-p-${blockIndex}`)}
      </p>,
    );
    blockIndex += 1;
  }

  return blocks;
}

function renderMarkdownBlocks(source: string, keyPrefix: string): ReactNode[] {
  const normalized = source.replace(/\r\n?/g, "\n");
  const parts = normalized.split(/(```[\s\S]*?```)/g).filter(Boolean);
  const blocks: ReactNode[] = [];
  let blockIndex = 0;

  for (const part of parts) {
    if (/^```/.test(part)) {
      const lines = part.split("\n");
      const firstLine = lines[0] ?? "```";
      const language = firstLine.slice(3).trim();
      const lastLine = lines[lines.length - 1]?.trim() === "```" ? lines.length - 1 : lines.length;
      const code = lines.slice(1, lastLine).join("\n");
      blocks.push(
        <pre key={`${keyPrefix}-code-${blockIndex}`}>
          <code className={language ? `language-${language}` : undefined}>{code}</code>
        </pre>,
      );
      blockIndex += 1;
      continue;
    }
    const textBlocks = renderTextBlocks(part, `${keyPrefix}-text-${blockIndex}`);
    blocks.push(...textBlocks.map((node, index) => <Fragment key={`${keyPrefix}-text-${blockIndex}-fragment-${index}`}>{node}</Fragment>));
    blockIndex += 1;
  }

  return blocks;
}

function appendFeedbackSuffix(
  blocks: ReactNode[],
  suffix: string,
  feedbackClassName: string,
  keyPrefix: string,
): ReactNode[] {
  if (!suffix) return blocks;
  const last = blocks[blocks.length - 1];
  const suffixNode = <span className={`chat-feedback-inline ${feedbackClassName}`}>{` ${suffix}`}</span>;
  if (last && isValidElement(last) && last.type === "p") {
    const paragraph = last as ReactElement<{ children?: ReactNode }>;
    return [
      ...blocks.slice(0, -1),
      cloneElement(paragraph, { key: paragraph.key ?? `${keyPrefix}-feedback-inline` }, <>{paragraph.props.children}{suffixNode}</>),
    ];
  }
  return [...blocks, <div key={`${keyPrefix}-feedback-suffix`} className={`chat-feedback-suffix ${feedbackClassName}`}>{suffix}</div>];
}

function messageSignature(messages: ChatMessage[]): string {
  return messages
    .map((row) => `${row.id}${row.type}${row.role ?? ""}${row.content}`)
    .join("");
}

function isNearBottom(node: HTMLDivElement): boolean {
  return node.scrollHeight - node.scrollTop - node.clientHeight <= 48;
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
  const previousSignatureRef = useRef("");
  const previousScrollKeyRef = useRef<string | undefined>(undefined);
  const stickToBottomRef = useRef(true);
  const signature = useMemo(() => messageSignature(messages), [messages]);

  useEffect(() => {
    const node = contentRef.current;
    if (!node) return;
    const updateStickiness = () => {
      stickToBottomRef.current = isNearBottom(node);
    };
    updateStickiness();
    node.addEventListener("scroll", updateStickiness);
    return () => node.removeEventListener("scroll", updateStickiness);
  }, []);

  useLayoutEffect(() => {
    const node = contentRef.current;
    if (!node) return;
    const scrollKeyChanged = previousScrollKeyRef.current !== scrollKey;
    const signatureChanged = previousSignatureRef.current !== signature;
    if (!scrollKeyChanged && !signatureChanged) return;
    if (scrollKeyChanged || stickToBottomRef.current) {
      node.scrollTop = node.scrollHeight;
      stickToBottomRef.current = true;
    }
    previousScrollKeyRef.current = scrollKey;
    previousSignatureRef.current = signature;
  }, [scrollKey, signature]);

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
          const contentBlocks = appendFeedbackSuffix(
            renderMarkdownBlocks(row.content, `${row.id}-content`),
            suffix,
            feedbackClassName,
            row.id,
          );
          return (
            <div key={row.id} className={`chat-row ${row.role === "responder" ? "chat-row-responder" : "chat-row-prompter"}`}>
              <div className={`${lineClassName} ${roleClassName}`}>
                <div className="chat-markdown">{contentBlocks}</div>
              </div>
            </div>
          );
        })}
    </div>
  );
}

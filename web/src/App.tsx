import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ChatComposer } from "./components/ChatComposer";
import { ChatThread } from "./components/ChatThread";
import { TurnstileWidget } from "./components/TurnstileWidget";

type OwnerType = "account";
type Page = "ask" | "dispatch" | "respond" | "leaderboard" | "account";
type RespondState = "poll" | "pool" | "active";

type AccountAuth = { mode: "account"; token: string };
type AuthState = AccountAuth;

type SessionItem = {
  id: string;
  title?: string;
  created_at: string;
  updated_at: string;
  message_count: number;
};

type MessageItem = {
  id: string;
  type: string;
  role?: string;
  content: string;
  created_at: string;
};

type JobItem = {
  id: string;
  status: string;
  created_at: string;
  session_id: string;
  response_message_id?: string;
  prompter_vote?: string;
  review_deadline_at?: string;
  claim_owner_type?: string;
  claim_owner_id?: string;
  claim_expires_at?: string;
};

type SessionState = {
  session_id: string;
  title?: string;
  state: "ready_for_prompt" | "waiting_for_responder" | "responder_working" | "feedback_required";
  can_send_message: boolean;
  can_vote: boolean;
  active_job?: JobItem & {
    tip_amount?: number;
    time_limit_minutes?: number;
    work_deadline_at?: string;
  };
};

type RoutingJob = {
  id: string;
  session_id: string;
  session_title?: string;
  session_snippet?: string;
  tip_amount: number;
  time_limit_minutes: number;
  is_rotated: boolean;
  routing_cycle_count: number;
  routing_started_at?: string;
  routing_ends_at?: string;
};

type AvailableResponder = {
  owner_type: OwnerType;
  owner_id: string;
  display_name?: string;
  last_seen_at: string;
  poll_started_at?: string;
  assignment_wait_seconds?: number;
  responder_description?: string;
};

type PoolCandidate = {
  id: string;
  session_id?: string;
  session_title?: string;
  session_snippet?: string;
  pool_started_at?: string;
  pool_ends_at?: string;
  tip_amount: number;
  time_limit_minutes: number;
};

type ResponderStatePayload =
  | { mode: "idle" }
  | { mode: "polling"; poll_started_at?: string; wait_until?: string; remaining_seconds?: number }
  | { mode: "pool"; candidates: PoolCandidate[] }
  | { mode: "assigned"; job_id: string };

type JobDetail = {
  id: string;
  status: string;
  tip_amount: number;
  time_limit_minutes: number;
  session_id: string;
  request_message_id: string;
  response_message_id?: string;
  prompter_vote?: string;
  review_deadline_at?: string;
  claim_expires_at?: string;
  work_deadline_at?: string;
  pool_started_at?: string;
  pool_ends_at?: string;
};

type ApiKeyItem = {
  id: string;
  label: string;
  created_at: string;
  last_used_at?: string;
};

type WalletInfo = {
  owner_type: OwnerType;
  owner_id: string;
  balance: number;
  last_refresh_at?: string;
};

type AccountStats = {
  job_success_rate: string;
  feedback_rate: string;
  dispatch_accuracy: string;
  jobs_completed: number;
  jobs_dispatched: number;
  responses_submitted: number;
};

type LeaderboardCategoryKey = "job_success_rate" | "dispatch_accuracy" | "total_available_credits";

type LeaderboardRow = {
  rank: number;
  account_id: string;
  account_name: string;
  metric_value: number;
  metric_display: string;
};

const API_BASE = (import.meta.env.VITE_API_BASE as string | undefined) ?? "http://localhost:8080";
const ACCOUNT_REGISTER_PATH =
  (import.meta.env.VITE_ACCOUNT_REGISTER_PATH as string | undefined) ?? "/_private/clawgrid-signup/accounts/register";
const TURNSTILE_SITEKEY = (import.meta.env.VITE_TURNSTILE_SITEKEY as string | undefined) ?? "";
const AUTH_KEY = "clawgrid_auth_v2";
const RESPOND_STATE_KEY_PREFIX = "clawgrid_respond_active_v1";
const DISPATCH_JOB_SLOTS = 4;
const DISPATCH_RESPONDER_SLOTS = 5;
const ACCOUNT_USERNAME_LIMIT = 40;
const ACCOUNT_EMAIL_MAX_BYTES = 320;
const ACCOUNT_PASSWORD_MIN_BYTES = 8;
const ACCOUNT_PASSWORD_MAX_BYTES = 72;

const leaderboardBoards: Array<{ key: LeaderboardCategoryKey; label: string }> = [
  { key: "job_success_rate", label: "job success rate" },
  { key: "dispatch_accuracy", label: "dispatch accuracy" },
  { key: "total_available_credits", label: "total available credits" },
];

async function api<T>(path: string, auth: AuthState | null, init?: RequestInit): Promise<T> {
  const headers = new Headers(init?.headers ?? {});
  headers.set("Accept", "application/json");
  if (!headers.has("Content-Type") && init?.body) headers.set("Content-Type", "application/json");
  if (auth?.mode === "account") headers.set("Authorization", `Bearer ${auth.token}`);

  const res = await fetch(`${API_BASE}${path}`, { ...init, headers, cache: "no-store", credentials: "include" });
  const text = await res.text();
  const parsed = text ? (JSON.parse(text) as unknown) : {};
  if (!res.ok) {
    const msg = typeof parsed === "object" && parsed !== null && "error" in parsed ? String((parsed as { error: string }).error) : `${res.status}`;
    throw new Error(msg);
  }
  return parsed as T;
}

function saveAuth(auth: AuthState | null): void {
  if (!auth) {
    localStorage.removeItem(AUTH_KEY);
    return;
  }
  localStorage.setItem(AUTH_KEY, JSON.stringify(auth));
}

function loadAuth(): AuthState | null {
  const raw = localStorage.getItem(AUTH_KEY);
  if (!raw) return null;
  try {
    const parsed = JSON.parse(raw) as {
      mode?: string;
      token?: string;
    };
    if (parsed.mode === "account" && typeof parsed.token === "string" && parsed.token) {
      return { mode: "account", token: parsed.token };
    }
    return null;
  } catch {
    return null;
  }
}

function fmtTime(v?: string): string {
  if (!v) return "-";
  return new Date(v).toLocaleString();
}

function formatCountdown(until: string | undefined, nowMs?: number): string {
  if (!until) return "0:00";
  const remaining = Math.max(0, Math.ceil((new Date(until).getTime() - (nowMs ?? Date.now())) / 1000));
  const mins = Math.floor(remaining / 60);
  const secs = remaining % 60;
  return `${mins}:${String(secs).padStart(2, "0")}`;
}

function clampPercent(v: number): number {
  if (Number.isNaN(v)) return 0;
  return Math.max(0, Math.min(100, v));
}

function normalizeEmailInput(raw: string): string {
  return raw.trim().toLowerCase();
}

function isValidEmailSyntax(email: string): boolean {
  if (!email || email.length > ACCOUNT_EMAIL_MAX_BYTES) return false;
  return /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email);
}

function formatCredits(v: number): string {
  return `${v.toFixed(2)} credits`;
}

function authIdentityKey(auth: AuthState | null): string | null {
  if (!auth) return null;
  return `account:${auth.token}`;
}

function transcriptPrefix(messageType: string, role?: string): string {
  if (messageType === "feedback") return "";
  if (role === "responder") return "responder";
  return "prompter";
}

function feedbackTranscriptSuffix(content: string): string {
  if (content === "good") return "(good response)";
  if (content === "bad") return "(bad response)";
  if (content === "no feedback") return "(no feedback)";
  if (content === "user rated reply as satisfactory" || content === "user rated response as satisfactory") return "(good response)";
  if (content === "user rated reply as unsatisfactory" || content === "user rated response as unsatisfactory") return "(bad response)";
  return `(${content})`;
}

function formatSessionTranscript(
  messages: Array<{ id: string; type: string; role?: string; content: string }>,
  awaitingFeedbackReplyToMessageId?: string,
): string {
  const feedbackByReplyId = new Map<string, string>();
  let lastResponderMessageID = "";
  for (const message of messages) {
    if (message.type === "feedback") {
      if (lastResponderMessageID) {
        feedbackByReplyId.set(lastResponderMessageID, feedbackTranscriptSuffix(message.content));
      }
      continue;
    }
    if (message.role === "responder") {
      lastResponderMessageID = message.id;
    }
  }

  return messages
    .filter((message) => message.type !== "feedback")
    .map((message) => {
      const prefix = transcriptPrefix(message.type, message.role);
      const suffix =
        feedbackByReplyId.get(message.id) ??
        (message.role === "responder" && awaitingFeedbackReplyToMessageId === message.id ? "(awaiting feedback)" : "");
      return `${prefix}: ${message.content}${suffix ? ` ${suffix}` : ""}`;
    })
    .join("\n");
}

function AskPage({ auth }: { auth: AuthState | null }) {
  const [sessions, setSessions] = useState<SessionItem[]>([]);
  const [selectedSessionId, setSelectedSessionId] = useState<string>("");
  const [messages, setMessages] = useState<MessageItem[]>([]);
  const [sessionState, setSessionState] = useState<SessionState | null>(null);
  const [input, setInput] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>("");
  const [timeLimitPreset, setTimeLimitPreset] = useState<string>("5");
  const [customTimeLimit, setCustomTimeLimit] = useState<string>("");
  const [tipPreset, setTipPreset] = useState<string>("0");
  const [customTip, setCustomTip] = useState<string>("");
  const [menuSessionId, setMenuSessionId] = useState<string>("");
  const [copyStatus, setCopyStatus] = useState<string>("");

  const loadSessions = async (preferredSelectedSessionId?: string) => {
    if (!auth) return;
    try {
      const data = await api<{ items: SessionItem[] }>("/sessions", auth);
      setSessions(data.items);
      const nextSelectedSessionId =
        preferredSelectedSessionId && data.items.some((row) => row.id === preferredSelectedSessionId)
          ? preferredSelectedSessionId
          : selectedSessionId;
      const selectedStillExists = data.items.some((row) => row.id === nextSelectedSessionId);
      if (nextSelectedSessionId && selectedStillExists) {
        setSelectedSessionId(nextSelectedSessionId);
      } else if (data.items[0]?.id) {
        setSelectedSessionId(data.items[0].id);
      } else {
        setSelectedSessionId("");
      }
    } catch (e) {
      setError((e as Error).message);
    }
  };

  const loadSelected = async (sessionId = selectedSessionId) => {
    if (!auth || !sessionId) {
      setMessages([]);
      setSessionState(null);
      return;
    }
    try {
      const [msgData, stateData] = await Promise.all([
        api<{ items: MessageItem[] }>(`/sessions/${sessionId}/messages`, auth),
        api<SessionState>(`/sessions/${sessionId}/state`, auth),
      ]);
      setMessages(msgData.items);
      setSessionState(stateData);
    } catch (e) {
      setError((e as Error).message);
    }
  };

  useEffect(() => {
    void loadSessions();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [auth]);

  useEffect(() => {
    void loadSelected();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [auth, selectedSessionId]);

  useEffect(() => {
    if (!auth || !selectedSessionId) return;
    const id = window.setInterval(() => {
      void loadSelected();
    }, 3000);
    return () => window.clearInterval(id);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [auth, selectedSessionId]);

  useEffect(() => {
    const onWindowClick = () => setMenuSessionId("");
    window.addEventListener("click", onWindowClick);
    return () => window.removeEventListener("click", onWindowClick);
  }, []);

  const pendingFeedbackJob = useMemo(() => {
    if (sessionState?.state !== "feedback_required") return undefined;
    return sessionState.active_job;
  }, [sessionState]);
  const awaitingFeedbackReplyToMessageId = pendingFeedbackJob?.response_message_id;

  const pendingOpenJob = useMemo(() => {
    if (sessionState?.state !== "waiting_for_responder" && sessionState?.state !== "responder_working") return undefined;
    return sessionState.active_job;
  }, [sessionState]);

  const pendingOpenJobStatus = useMemo(() => {
    if (!pendingOpenJob) return "";
    if (sessionState?.state === "responder_working") return "a responder is working on a reply";
    return "waiting for a responder";
  }, [pendingOpenJob, sessionState]);

  const createSessionRecord = async (title?: string): Promise<string | null> => {
    if (!auth) return null;
    const payload = title?.trim() ? { title: title.trim() } : undefined;
    const data = await api<{ id: string }>("/sessions", auth, {
      method: "POST",
      body: payload ? JSON.stringify(payload) : undefined,
    });
    return data.id;
  };

  const createSession = async () => {
    if (!auth) return;
    setBusy(true);
    setError("");
    try {
      const sessionId = await createSessionRecord();
      if (!sessionId) return;
      await loadSessions(sessionId);
      await loadSelected(sessionId);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const sendMessage = async () => {
    if (!auth || !input.trim()) return;
    const customParsed = Number.parseInt(customTimeLimit, 10);
    const selectedLimit =
      timeLimitPreset === "custom"
        ? (Number.isFinite(customParsed) && customParsed > 0 ? customParsed : 0)
        : Number.parseInt(timeLimitPreset, 10);
    const customTipParsed = Number.parseFloat(customTip);
    const selectedTip =
      tipPreset === "custom"
        ? (Number.isFinite(customTipParsed) && customTipParsed > 0 ? customTipParsed : 0)
        : Number.parseFloat(tipPreset);
    setBusy(true);
    setError("");
    try {
      let targetSessionId = selectedSessionId;
      if (!targetSessionId) {
        const createdSessionId = await createSessionRecord();
        if (!createdSessionId) {
          throw new Error("failed_to_create_session");
        }
        targetSessionId = createdSessionId;
      }
      await api(`/sessions/${targetSessionId}/messages`, auth, {
        method: "POST",
        body: JSON.stringify({
          type: "text",
          content: input.trim(),
          tip_amount: selectedTip,
          time_limit_minutes: selectedLimit,
        }),
      });
      setInput("");
      setSelectedSessionId(targetSessionId);
      await loadSelected(targetSessionId);
      await loadSessions(targetSessionId);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const vote = async (voteValue: "up" | "down") => {
    if (!auth || !pendingFeedbackJob) return;
    setBusy(true);
    setError("");
    try {
      await api(`/jobs/${pendingFeedbackJob.id}/vote`, auth, {
        method: "POST",
        body: JSON.stringify({ vote: voteValue }),
      });
      await loadSelected();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const cancelWaiting = async () => {
    if (!auth || !pendingOpenJob) return;
    setBusy(true);
    setError("");
    try {
      const data = await api<{ penalized?: boolean; penalty_amount?: number }>(`/jobs/${pendingOpenJob.id}/cancel`, auth, { method: "POST" });
      if (data.penalized) {
        setError(`waiting cancelled. penalty applied: ${Number(data.penalty_amount ?? 0).toFixed(2)} credits`);
      }
      await loadSelected();
      await loadSessions();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const deleteSession = async (sessionID: string) => {
    if (!auth) return;
    setBusy(true);
    setError("");
    setMenuSessionId("");
    try {
      await api(`/sessions/${sessionID}`, auth, { method: "DELETE" });
      if (selectedSessionId === sessionID) {
        setSelectedSessionId("");
        setMessages([]);
        setSessionState(null);
      }
      await loadSessions();
    } catch (e) {
      const message = (e as Error).message;
      if (message === "session_has_unresolved_jobs") {
        window.alert("This session cannot be deleted yet because it still has unresolved work or pending feedback. Finish, cancel, or review the active job first.");
      } else if (message === "session not found") {
        window.alert("This session no longer exists.");
      }
      setError(message);
      await loadSessions();
    } finally {
      setBusy(false);
    }
  };

  const renameSession = async (sessionID: string, currentTitle?: string) => {
    if (!auth) return;
    const nextTitle = window.prompt("Rename session", currentTitle?.trim() ? currentTitle : sessionID);
    if (nextTitle == null) {
      setMenuSessionId("");
      return;
    }
    setBusy(true);
    setError("");
    setMenuSessionId("");
    try {
      await api(`/sessions/${sessionID}`, auth, {
        method: "PATCH",
        body: JSON.stringify({ title: nextTitle.trim() }),
      });
      await loadSessions();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const copySessionTranscript = async () => {
    if (!messages.length) return;
    try {
      await navigator.clipboard.writeText(formatSessionTranscript(messages, awaitingFeedbackReplyToMessageId));
      setCopyStatus("copied");
      window.setTimeout(() => setCopyStatus(""), 1500);
    } catch {
      setError("copy_failed");
    }
  };

  return (
    <main className="ask-layout">
      <aside className="session-rail" aria-label="Sessions">
        <button className="new-session" onClick={createSession} disabled={busy || !auth}>
          new session
        </button>

        <div className="session-list">
          {sessions.map((s) => (
            <div
              className={`session-item ${selectedSessionId === s.id ? "active-item" : ""}`}
              key={s.id}
            >
              <button
                className="session-main"
                onClick={() => setSelectedSessionId(s.id)}
                title={s.title?.trim() ? s.title : s.id}
              >
                {s.title?.trim() ? s.title : s.id}
              </button>
              <button
                className="session-more"
                onClick={(e) => {
                  e.stopPropagation();
                  setMenuSessionId((current) => (current === s.id ? "" : s.id));
                }}
                aria-label="More options"
                type="button"
              >
                ...
              </button>
              {menuSessionId === s.id && (
                <div className="session-menu" onClick={(e) => e.stopPropagation()}>
                  <button className="session-menu-item" type="button" onClick={() => void renameSession(s.id, s.title)} disabled={busy}>
                    rename
                  </button>
                  <button className="session-menu-item danger" type="button" onClick={() => void deleteSession(s.id)} disabled={busy}>
                    delete
                  </button>
                </div>
              )}
            </div>
          ))}
        </div>

        <p className="session-more-label">{sessions.length > 0 ? `${sessions.length} total` : "no sessions"}</p>
      </aside>

      <section className="thread-panel" aria-label="Conversation">
        <div className="thread-head">
          <p className="thread-session-id">{selectedSessionId ? `session: ${selectedSessionId}` : "session: -"}</p>
          <button className="account-btn small thread-copy-btn" onClick={() => void copySessionTranscript()} disabled={!messages.length}>
            {copyStatus || "copy"}
          </button>
        </div>
        <ChatThread
          messages={messages}
          scrollKey={selectedSessionId}
          className="thread-content"
          lineClassName="thread-line"
          awaitingFeedbackReplyToMessageId={awaitingFeedbackReplyToMessageId}
        />

        {pendingFeedbackJob ? (
          <div className="feedback-row" aria-label="Feedback actions">
            <button className="feedback-btn" disabled={busy} onClick={() => vote("up")}>good response</button>
            <button className="feedback-btn" disabled={busy} onClick={() => vote("down")}>bad response</button>
          </div>
        ) : pendingOpenJob ? (
          <div className="waiting-panel" aria-label="Pending job status">
            <p className="waiting-title">{pendingOpenJobStatus}</p>
            <p className="waiting-meta">job: {pendingOpenJob.id}</p>
            <button className="account-btn small danger" disabled={busy} onClick={() => void cancelWaiting()}>
              cancel waiting
            </button>
          </div>
        ) : (
          <div className="ask-compose-stack">
            <div className="time-limit-row">
              <span className="time-limit-label">time limit (minutes):</span>
              {["1", "5", "10", "30", "custom"].map((opt) => (
                <button
                  key={opt}
                  className={`time-limit-btn ${timeLimitPreset === opt ? "active" : ""}`}
                  onClick={() => setTimeLimitPreset(opt)}
                  type="button"
                >
                  {opt}
                </button>
              ))}
              {timeLimitPreset === "custom" && (
                <input
                  className="time-limit-custom"
                  type="number"
                  min={1}
                  value={customTimeLimit}
                  onChange={(e) => setCustomTimeLimit(e.target.value)}
                  placeholder="minutes"
                />
              )}
            </div>

            <div className="time-limit-row">
              <span className="time-limit-label">bonus tip:</span>
              {["0", "0.5", "1", "2", "custom"].map((opt) => (
                <button
                  key={opt}
                  className={`time-limit-btn ${tipPreset === opt ? "active" : ""}`}
                  onClick={() => setTipPreset(opt)}
                  type="button"
                >
                  {opt}
                </button>
              ))}
              {tipPreset === "custom" && (
                <input
                  className="time-limit-custom"
                  type="number"
                  min={0}
                  step="0.1"
                  value={customTip}
                  onChange={(e) => setCustomTip(e.target.value)}
                  placeholder="credits"
                />
              )}
            </div>

            <ChatComposer
              value={input}
              onChange={setInput}
              onSubmit={() => void sendMessage()}
              disabled={busy}
              placeholder="enter text here. enter sends, shift+enter adds a new line"
              className="thread-composer chat-composer-shell"
              textareaClassName="thread-input chat-textarea"
              sendButtonClassName="thread-send"
              sendLabel="send"
            />
          </div>
        )}

        {error && <p className="inline-error">{error}</p>}
      </section>
    </main>
  );
}

function DispatchPage({ auth }: { auth: AuthState | null }) {
  const [jobs, setJobs] = useState<RoutingJob[]>([]);
  const [responders, setResponders] = useState<AvailableResponder[]>([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [draggingResponder, setDraggingResponder] = useState<AvailableResponder | null>(null);
  const [dragPos, setDragPos] = useState<{ x: number; y: number } | null>(null);
  const [hoveredJobID, setHoveredJobID] = useState<string>("");
  const [nowTick, setNowTick] = useState(() => Date.now());
  const jobSlots = Array.from({ length: DISPATCH_JOB_SLOTS }, (_, index) => jobs[index] ?? null);
  const responderSlots = Array.from({ length: DISPATCH_RESPONDER_SLOTS }, (_, index) => responders[index] ?? null);

  const clearDragState = () => {
    setDraggingResponder(null);
    setDragPos(null);
    setHoveredJobID("");
  };

  const findJobDropAt = (x: number, y: number): string => {
    const el = document.elementFromPoint(x, y);
    const dropZone = el?.closest("[data-job-drop='true']") as HTMLElement | null;
    return dropZone?.dataset.jobId ?? "";
  };

  const load = async () => {
    if (!auth) return;
    try {
      const [jobData, responderData] = await Promise.all([
        api<{ items: RoutingJob[] }>("/routing/jobs", auth),
        api<{ items: AvailableResponder[] }>("/responders/available", auth),
      ]);
      setJobs(jobData.items);
      setResponders(responderData.items);
    } catch (e) {
      setError((e as Error).message);
    }
  };

  useEffect(() => {
    void load();
    const id = window.setInterval(() => {
      void load();
    }, 3000);
    return () => window.clearInterval(id);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [auth]);

  useEffect(() => {
    const id = window.setInterval(() => setNowTick(Date.now()), 1000);
    return () => window.clearInterval(id);
  }, []);

  useEffect(() => {
    if (!draggingResponder) return;
    const prevUserSelect = document.body.style.userSelect;
    document.body.style.userSelect = "none";

    const handleMove = (e: PointerEvent) => {
      setDragPos({ x: e.clientX, y: e.clientY });
      setHoveredJobID(findJobDropAt(e.clientX, e.clientY));
    };

    const handleUp = (e: PointerEvent) => {
      const jobID = findJobDropAt(e.clientX, e.clientY);
      const responder = draggingResponder;
      clearDragState();
      if (jobID) void assign(jobID, responder);
    };

    const handleCancel = () => {
      clearDragState();
    };

    window.addEventListener("pointermove", handleMove);
    window.addEventListener("pointerup", handleUp);
    window.addEventListener("pointercancel", handleCancel);

    return () => {
      document.body.style.userSelect = prevUserSelect;
      window.removeEventListener("pointermove", handleMove);
      window.removeEventListener("pointerup", handleUp);
      window.removeEventListener("pointercancel", handleCancel);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [draggingResponder]);

  const assign = async (jobID: string, responder: AvailableResponder) => {
    if (!auth) return;
    setBusy(true);
    setError("");
    try {
      await api("/assignments", auth, {
        method: "POST",
        body: JSON.stringify({
          job_id: jobID,
          responder_owner_id: responder.owner_id,
        }),
      });
      await load();
    } catch (e) {
      const msg = (e as Error).message;
      if (msg === "dispatcher_cannot_assign_self") {
        setError("cannot assign to your own responder identity");
      } else if (msg === "prompter_cannot_be_responder") {
        setError("job creator cannot also be the responder");
      } else if (msg === "dispatcher_cannot_assign_own_jobs") {
        setError("backend is running old assignment rules; rebuild/restart the api container");
      } else {
        setError(msg);
      }
    } finally {
      setBusy(false);
    }
  };

  return (
    <main className="dispatch-layout">
      <section className="dispatch-section dispatch-section-fill">
        <div className="section-head-row">
          <h2 className="dispatch-title">Jobs in routing</h2>
          <p className="dispatch-hint">drag a responder onto a job card to assign</p>
        </div>
        <div className="jobs-grid">
          {jobSlots.map((job, index) => {
            if (!job) {
              return <div className="dispatch-card dispatch-card-empty" key={`job-slot-${index}`}>EMPTY</div>;
            }
            const started = job.routing_started_at ? new Date(job.routing_started_at).getTime() : nowTick;
            const ends = job.routing_ends_at ? new Date(job.routing_ends_at).getTime() : nowTick;
            const total = Math.max(1, ends - started);
            const elapsed = clampPercent(((nowTick - started) / total) * 100);
            const title = job.session_title?.trim() ? job.session_title : job.session_id;
            return (
            <div
              className={`dispatch-card dispatch-job-slot ${hoveredJobID === job.id ? "drop-hover" : ""}`}
              key={job.id}
              data-job-drop="true"
              data-job-id={job.id}
            >
              <div className="dispatch-card-pill" title={title}>{title}</div>
              <p className="dispatch-card-snippet">{job.session_snippet?.trim() ? job.session_snippet : "no recent messages"}</p>
              <p className="dispatch-card-meta">time limit: {job.time_limit_minutes > 0 ? `${job.time_limit_minutes}m` : "-"}</p>
              {job.tip_amount > 0 && <p className="dispatch-card-meta">bonus tip: {formatCredits(job.tip_amount)}</p>}
              <div className="dispatch-progress" aria-hidden="true">
                <div className="dispatch-progress-fill" style={{ width: `${elapsed}%` }} />
              </div>
            </div>
          )})}
        </div>
      </section>

      <section className="dispatch-section">
        <h2 className="dispatch-title">Available responders</h2>
        <div className="responders-grid">
          {responderSlots.map((responder, index) => {
            if (!responder) {
              return <div className="dispatch-responder-card dispatch-responder-empty" key={`responder-slot-${index}`}>VACANT</div>;
            }
            const isDraggingSource = Boolean(
              draggingResponder &&
              draggingResponder.owner_type === responder.owner_type &&
              draggingResponder.owner_id === responder.owner_id,
            );
            const started = responder.poll_started_at ? new Date(responder.poll_started_at).getTime() : nowTick;
            const totalSeconds = responder.assignment_wait_seconds ?? 30;
            const total = Math.max(1000, totalSeconds * 1000);
            const elapsed = clampPercent(((nowTick - started) / total) * 100);
            return (
              <div
                className={`dispatch-responder-card ${isDraggingSource ? "drag-source-hidden" : ""}`}
                key={`${responder.owner_type}:${responder.owner_id}`}
                onPointerDown={(e) => {
                  if (busy || e.button !== 0) return;
                  e.preventDefault();
                  setError("");
                  setDraggingResponder(responder);
                  setDragPos({ x: e.clientX, y: e.clientY });
                  setHoveredJobID("");
                }}
              >
                <p className="dispatch-responder-name">{responder.display_name?.trim() ? responder.display_name : responder.owner_id}</p>
                <p className="dispatch-responder-blurb">
                  {responder.responder_description?.trim() ? responder.responder_description : "no responder blurb yet"}
                </p>
                <p className="dispatch-card-meta">{responder.owner_id}</p>
                <div className="dispatch-progress" aria-hidden="true">
                  <div className="dispatch-progress-fill" style={{ width: `${elapsed}%` }} />
                </div>
              </div>
            );
          })}
        </div>
      </section>

      {draggingResponder && dragPos && (
        <div className="dispatch-drag-floating" style={{ left: dragPos.x + 10, top: dragPos.y + 10 }}>
          <p className="dispatch-responder-name">{draggingResponder.display_name?.trim() ? draggingResponder.display_name : draggingResponder.owner_id}</p>
          <p className="dispatch-responder-blurb">
            {draggingResponder.responder_description?.trim() ? draggingResponder.responder_description : "no responder blurb yet"}
          </p>
        </div>
      )}

      {error && <p className="inline-error">{error}</p>}
    </main>
  );
}

function RespondPage({ auth }: { auth: AuthState | null }) {
  const [respondState, setRespondState] = useState<RespondState>("poll");
  const [candidates, setCandidates] = useState<PoolCandidate[]>([]);
  const [activeJob, setActiveJob] = useState<JobDetail | null>(null);
  const [activeMessages, setActiveMessages] = useState<MessageItem[]>([]);
  const [reply, setReply] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [countdownNow, setCountdownNow] = useState(() => Date.now());
  const [copyStatus, setCopyStatus] = useState<string>("");
  const [pollingOrigin, setPollingOrigin] = useState<"idle" | "local" | "external">("idle");
  const [externalWaitUntil, setExternalWaitUntil] = useState<string>("");
  const identityKey = authIdentityKey(auth);
  const pollAbortRef = useRef<AbortController | null>(null);

  const clearPersistedActive = () => {
    if (!identityKey) return;
    localStorage.removeItem(`${RESPOND_STATE_KEY_PREFIX}:${identityKey}`);
  };

  const clearActiveView = () => {
    setActiveJob(null);
    setActiveMessages([]);
    setReply("");
    clearPersistedActive();
  };

  const persistActive = (payload: { job_id: string; claim_expires_at?: string }) => {
    if (!identityKey) return;
    localStorage.setItem(`${RESPOND_STATE_KEY_PREFIX}:${identityKey}`, JSON.stringify(payload));
  };

  const openJob = async (jobID: string, opts?: { claimFirst?: boolean }) => {
    if (!auth) return;
    setBusy(true);
    setError("");
    try {
      if (opts?.claimFirst) {
        await api(`/jobs/${jobID}/claim`, auth, { method: "POST" });
      }
      const job = await api<JobDetail>(`/jobs/${jobID}`, auth);
      const msg = await api<{ items: MessageItem[] }>(`/sessions/${job.session_id}/messages`, auth);
      setActiveJob(job);
      setActiveMessages(msg.items);
      setRespondState("active");
      persistActive({ job_id: job.id, claim_expires_at: job.claim_expires_at });
    } catch (e) {
      const msg = (e as Error).message;
      if (opts?.claimFirst && (msg === "job_not_pool" || msg === "job_already_claimed" || msg === "forbidden")) {
        setCandidates((current) => {
          const next = current.filter((candidate) => candidate.id !== jobID);
          if (!next.length) {
            setRespondState("poll");
            setError("no jobs found");
          }
          return next;
        });
        clearPersistedActive();
        return;
      }
      setError(msg);
      clearPersistedActive();
    } finally {
      setBusy(false);
    }
  };

  const poll = async () => {
    if (!auth) return;
    pollAbortRef.current?.abort();
    const controller = new AbortController();
    let externalPollDetected = false;
    pollAbortRef.current = controller;
    setPollingOrigin("local");
    setExternalWaitUntil("");
    setBusy(true);
    setError("");
    setCandidates([]);
    try {
      const data = await api<{ mode: "assigned" | "pool"; job_id?: string; candidates?: PoolCandidate[] }>(
        "/responders/work",
        auth,
        { signal: controller.signal },
      );
      if (data.mode === "assigned" && data.job_id) {
        await openJob(data.job_id);
        return;
      }
      const nextCandidates = data.candidates ?? [];
      setPollingOrigin("idle");
      setCandidates(nextCandidates);
      if (!nextCandidates.length) {
        setRespondState("poll");
        setError("no jobs found");
        return;
      }
      setRespondState("pool");
      return;
    } catch (e) {
      if ((e as Error).name === "AbortError") {
        setError("");
        setPollingOrigin("idle");
        setExternalWaitUntil("");
        return;
      }
      const msg = (e as Error).message;
      if (msg === "already_polling") {
        externalPollDetected = true;
        setPollingOrigin("external");
        setError("");
        return;
      }
      setError(msg);
    } finally {
      if (pollAbortRef.current === controller) {
        pollAbortRef.current = null;
      }
      if (!externalPollDetected) {
        setPollingOrigin("idle");
        setExternalWaitUntil("");
      }
      setBusy(false);
    }
  };

  const cancelPoll = () => {
    pollAbortRef.current?.abort();
    pollAbortRef.current = null;
    setBusy(false);
    setCandidates([]);
    setError("");
    setPollingOrigin("idle");
    setExternalWaitUntil("");
  };

  const submitReply = async () => {
    if (!auth || !activeJob || !reply.trim()) return;
    setBusy(true);
    setError("");
    try {
      await api(`/jobs/${activeJob.id}/reply`, auth, {
        method: "POST",
        body: JSON.stringify({ content: reply.trim() }),
      });
      clearActiveView();
      setCandidates([]);
      setRespondState("poll");
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const copySessionTranscript = async () => {
    if (!activeMessages.length) return;
    try {
      await navigator.clipboard.writeText(formatSessionTranscript(activeMessages));
      setCopyStatus("copied");
      window.setTimeout(() => setCopyStatus(""), 1500);
    } catch {
      setError("copy_failed");
    }
  };

  useEffect(() => {
    if (!auth || !identityKey) return;
    const raw = localStorage.getItem(`${RESPOND_STATE_KEY_PREFIX}:${identityKey}`);
    if (!raw) return;
    try {
      const saved = JSON.parse(raw) as { job_id?: string; claim_expires_at?: string };
      if (!saved.job_id) {
        clearPersistedActive();
        return;
      }
      if (saved.claim_expires_at && new Date(saved.claim_expires_at).getTime() <= Date.now()) {
        clearPersistedActive();
        return;
      }
      void openJob(saved.job_id);
    } catch {
      clearPersistedActive();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [auth, identityKey]);

  useEffect(() => {
    if (!auth || respondState !== "poll" || pollingOrigin === "local") return;
    let active = true;

    const syncResponderState = async () => {
      try {
        const data = await api<ResponderStatePayload>("/responders/state", auth);
        if (!active) return;
        if (data.mode === "assigned") {
          setPollingOrigin("idle");
          setExternalWaitUntil("");
          await openJob(data.job_id);
          return;
        }
        if (data.mode === "pool") {
          setPollingOrigin("idle");
          setExternalWaitUntil("");
          setCandidates(data.candidates ?? []);
          if ((data.candidates ?? []).length > 0) {
            setRespondState("pool");
          }
          return;
        }
        if (data.mode === "polling") {
          setPollingOrigin("external");
          setExternalWaitUntil(data.wait_until ?? "");
          return;
        }
        setPollingOrigin("idle");
        setExternalWaitUntil("");
      } catch (e) {
        if (!active) return;
        setError((e as Error).message);
      }
    };

    void syncResponderState();
    const id = window.setInterval(() => {
      void syncResponderState();
    }, 2000);

    return () => {
      active = false;
      window.clearInterval(id);
    };
  }, [auth, respondState, pollingOrigin]);

  useEffect(() => {
    if (!auth || respondState !== "active" || !activeJob?.id) return;
    let active = true;

    const syncActiveResponderState = async () => {
      try {
        const data = await api<ResponderStatePayload>("/responders/state", auth);
        if (!active) return;
        if (data.mode === "assigned") {
          if (data.job_id !== activeJob.id) {
            await openJob(data.job_id);
          }
          return;
        }
        if (data.mode === "pool") {
          clearActiveView();
          setPollingOrigin("idle");
          setExternalWaitUntil("");
          const nextCandidates = data.candidates ?? [];
          setCandidates(nextCandidates);
          if (nextCandidates.length > 0) {
            setRespondState("pool");
          } else {
            setRespondState("poll");
            setError("no jobs found");
          }
          return;
        }
        if (data.mode === "polling") {
          clearActiveView();
          setCandidates([]);
          setRespondState("poll");
          setPollingOrigin("external");
          setExternalWaitUntil(data.wait_until ?? "");
          return;
        }
        clearActiveView();
        setCandidates([]);
        setRespondState("poll");
        setPollingOrigin("idle");
        setExternalWaitUntil("");
      } catch (e) {
        if (!active) return;
        setError((e as Error).message);
      }
    };

    const id = window.setInterval(() => {
      void syncActiveResponderState();
    }, 2000);

    return () => {
      active = false;
      window.clearInterval(id);
    };
  }, [auth, respondState, activeJob?.id]);

  useEffect(() => {
    if (respondState !== "active" || !activeJob?.work_deadline_at) return;
    const expiresAt = new Date(activeJob.work_deadline_at).getTime();
    if (Number.isNaN(expiresAt)) return;
    const timerID = window.setInterval(() => {
      if (Date.now() < expiresAt) return;
      clearActiveView();
      setCandidates([]);
      setRespondState("poll");
    }, 1000);
    return () => window.clearInterval(timerID);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [respondState, activeJob?.id, activeJob?.work_deadline_at]);

  useEffect(() => {
    if (respondState !== "active" && respondState !== "pool" && pollingOrigin === "idle") return;
    const id = window.setInterval(() => setCountdownNow(Date.now()), 1000);
    return () => window.clearInterval(id);
  }, [respondState, pollingOrigin]);

  useEffect(() => {
    if (respondState !== "pool" || !candidates.length) return;
    const now = Date.now();
    const next = candidates.filter((candidate) => {
      if (!candidate.pool_ends_at) return true;
      return new Date(candidate.pool_ends_at).getTime() > now;
    });
    if (next.length === candidates.length) return;
    setCandidates(next);
    if (!next.length) {
      setRespondState("poll");
      setError("no jobs found");
    }
  }, [respondState, countdownNow, candidates]);

  useEffect(() => {
    return () => {
      pollAbortRef.current?.abort();
    };
  }, []);

  if (respondState === "poll") {
    const isWaiting = busy || pollingOrigin !== "idle";
    const waitingExternally = pollingOrigin === "external";
    return (
      <main className="respond-poll-layout">
        <div className="respond-poll-center">
          <button className="respond-main-btn" onClick={() => void poll()} disabled={isWaiting || !auth}>
            poll jobs
          </button>
          {busy && pollingOrigin === "local" && (
            <button className="respond-sub-btn" onClick={cancelPoll} type="button">
              cancel
            </button>
          )}
        </div>
        <p className="respond-help-text">
          {isWaiting
            ? waitingExternally
              ? `already polling from another client${externalWaitUntil ? ` until ${formatCountdown(externalWaitUntil, countdownNow)}` : ""}.`
              : "waiting for direct assignment before showing system pool."
            : "polling checks direct assignment first. if none arrives in the wait window, system pool options are shown."}
        </p>
        {error && <p className="inline-error">{error}</p>}
      </main>
    );
  }

  if (respondState === "pool") {
    return (
      <main className="respond-pool-layout">
        <div className="respond-pool-head">
          <h2 className="respond-pool-title">Select job from the following</h2>
        </div>

        <div className="respond-pool-grid">
          {candidates.map((candidate) => {
            const started = candidate.pool_started_at ? new Date(candidate.pool_started_at).getTime() : countdownNow;
            const ends = candidate.pool_ends_at ? new Date(candidate.pool_ends_at).getTime() : countdownNow;
            const total = Math.max(1, ends - started);
            const elapsed = clampPercent(((countdownNow - started) / total) * 100);
            const title = candidate.session_title?.trim() ? candidate.session_title : candidate.session_id ?? candidate.id;
            return (
            <button className="dispatch-card dispatch-job-slot respond-pool-job-card" key={candidate.id} onClick={() => void openJob(candidate.id, { claimFirst: true })} disabled={busy}>
              <div className="dispatch-card-pill" title={title}>{title}</div>
              <p className="dispatch-card-snippet">{candidate.session_snippet?.trim() ? candidate.session_snippet : "no recent messages"}</p>
              <p className="dispatch-card-meta">time limit: {candidate.time_limit_minutes > 0 ? `${candidate.time_limit_minutes}m` : "-"}</p>
              {candidate.tip_amount > 0 && <p className="dispatch-card-meta">bonus tip: {formatCredits(candidate.tip_amount)}</p>}
              <div className="dispatch-progress" aria-hidden="true">
                <div className="dispatch-progress-fill" style={{ width: `${elapsed}%` }} />
              </div>
            </button>
          );})}
          {!candidates.length && <div className="placeholder-box">no pool jobs available</div>}
        </div>

        <p className="respond-help-text">system pool appears only after polling yields no direct assignment.</p>
        {error && <p className="inline-error">{error}</p>}
      </main>
    );
  }

  return (
    <main className="respond-active-layout">
      <aside className="respond-side">
        <div className="respond-side-box">job: {activeJob?.id}</div>
        <div className="respond-side-box">status: {activeJob?.status}</div>
        <div className="respond-side-box">timer: {formatCountdown(activeJob?.work_deadline_at, countdownNow)}</div>
        <div className="respond-side-box">session: {activeJob?.session_id}</div>
      </aside>

      <section className="respond-thread-panel">
        <div className="thread-head">
          <p className="thread-session-id">session: {activeJob?.session_id ?? "-"}</p>
          <button className="account-btn small thread-copy-btn" onClick={() => void copySessionTranscript()} disabled={!activeMessages.length}>
            {copyStatus || "copy"}
          </button>
        </div>
        <ChatThread
          messages={activeMessages}
          scrollKey={activeJob?.id}
          className="respond-thread-content"
          lineClassName="respond-thread-line"
        />

        <ChatComposer
          value={reply}
          onChange={setReply}
          onSubmit={() => void submitReply()}
          disabled={busy}
          placeholder="enter text here. enter sends, shift+enter adds a new line"
          className="respond-composer chat-composer-shell"
          textareaClassName="respond-composer-input chat-textarea"
          sendButtonClassName="thread-send respond-send"
          sendLabel="reply"
          leadingSlot={<button className="respond-attach-btn" aria-label="Add attachment" type="button">+</button>}
          maxHeight={170}
        />
        <p className="respond-help-text compact">submitting a response completes this job.</p>
        {error && <p className="inline-error">{error}</p>}
      </section>
    </main>
  );
}

function LeaderboardPage() {
  const [activeBoard, setActiveBoard] = useState<LeaderboardCategoryKey>(leaderboardBoards[0].key);
  const [rows, setRows] = useState<LeaderboardRow[]>([]);
  const [qualificationRule, setQualificationRule] = useState("");
  const [refreshedAt, setRefreshedAt] = useState("");
  const [error, setError] = useState("");

  useEffect(() => {
    const load = async () => {
      try {
        const data = await api<{
          refreshed_at: string;
          qualification_rule: string;
          items: LeaderboardRow[];
        }>(`/leaderboards?category=${encodeURIComponent(activeBoard)}`, null);
        setRows(data.items);
        setQualificationRule(data.qualification_rule);
        setRefreshedAt(data.refreshed_at);
        setError("");
      } catch (e) {
        setRows([]);
        setError((e as Error).message);
      }
    };
    void load();
  }, [activeBoard]);

  return (
    <main className="leaderboard-layout">
      <div className="leaderboard-head">
        <h2 className="leaderboard-title">leaderboards</h2>
        <p className="leaderboard-refresh">{refreshedAt ? `last refreshed ${fmtTime(refreshedAt)}` : "loading..."}</p>
      </div>

      <div className="leaderboard-tabs">
        {leaderboardBoards.map((board) => (
          <button
            className={`leaderboard-tab ${activeBoard === board.key ? "active" : ""}`}
            key={board.key}
            onClick={() => setActiveBoard(board.key)}
          >
            {board.label}
          </button>
        ))}
      </div>

      <section className="leaderboard-panel">
        <p className="leaderboard-qualification">{qualificationRule}</p>
        <div className="leaderboard-table-head">
          <span>rank</span>
          <span>account</span>
          <span>{leaderboardBoards.find((board) => board.key === activeBoard)?.label ?? activeBoard}</span>
        </div>

        <div className="leaderboard-table-body">
          {rows.map((row) => (
            <div className="leaderboard-row" key={`${activeBoard}-${row.account_id}`}>
              <span>#{row.rank}</span>
              <span>{row.account_name}</span>
              <span>{row.metric_display}</span>
            </div>
          ))}
          {!rows.length && !error && <div className="leaderboard-empty">no qualified accounts yet</div>}
        </div>
        {error && <p className="inline-error">{error}</p>}
      </section>
    </main>
  );
}

function AccountPage({ auth, setAuth }: { auth: AuthState | null; setAuth: (a: AuthState | null) => void }) {
  const [loginNameInput, setLoginNameInput] = useState("");
  const [loginPasswordInput, setLoginPasswordInput] = useState("");
  const [nameInput, setNameInput] = useState("");
  const [emailInput, setEmailInput] = useState("");
  const [passwordInput, setPasswordInput] = useState("");
  const [signupTurnstileToken, setSignupTurnstileToken] = useState("");
  const [signupTurnstileResetNonce, setSignupTurnstileResetNonce] = useState(0);
  const [signupModalOpen, setSignupModalOpen] = useState(false);
  const [signupBusy, setSignupBusy] = useState(false);
  const [accountName, setAccountName] = useState("-");
  const [accountID, setAccountID] = useState("-");
  const [accountEmail, setAccountEmail] = useState("");
  const [wallet, setWallet] = useState<WalletInfo | null>(null);
  const [keys, setKeys] = useState<ApiKeyItem[]>([]);
  const [stats, setStats] = useState<AccountStats | null>(null);
  const [error, setError] = useState("");
  const [responderDescription, setResponderDescription] = useState("");

  const isSignedOut = !auth;
  const signupNeedsTurnstile = TURNSTILE_SITEKEY.trim() !== "";
  const responderDescriptionChars = Array.from(responderDescription).length;

  const handleSignupTurnstileTokenChange = useCallback((token: string) => {
    setSignupTurnstileToken(token);
  }, []);

  const handleSignupTurnstileError = useCallback((message: string) => {
    setError(message);
  }, []);

  useEffect(() => {
    if (!signupModalOpen || !signupTurnstileToken || signupBusy) return;
    void submitSignUp(signupTurnstileToken);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [signupModalOpen, signupTurnstileToken, signupBusy]);

  const loadAccountData = async () => {
    if (!auth || auth.mode !== "account") return;
    try {
      const [me, walletData, keyData, statsData] = await Promise.all([
        api<{ id: string; name: string; email?: string; responder_description?: string }>("/account/me", auth),
        api<WalletInfo>("/wallets/current", auth),
        api<{ items: ApiKeyItem[] }>("/account/api-keys", auth),
        api<AccountStats>("/account/stats", auth),
      ]);
      setAccountID(me.id);
      setAccountName(me.name);
      setAccountEmail(me.email ?? "");
      setResponderDescription(me.responder_description ?? "");
      setWallet(walletData);
      setKeys(keyData.items);
      setStats(statsData);
    } catch (e) {
      setError((e as Error).message);
    }
  };

  useEffect(() => {
    void loadAccountData();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [auth]);

  const submitSignUp = async (turnstileToken: string) => {
    setSignupBusy(true);
    try {
      const data = await api<{ account_id: string; api_key: string; session_token: string }>(ACCOUNT_REGISTER_PATH, null, {
        method: "POST",
        body: JSON.stringify({
          name: nameInput,
          email: normalizeEmailInput(emailInput),
          password: passwordInput,
          turnstile_token: turnstileToken,
        }),
      });
      const next: AccountAuth = { mode: "account", token: data.session_token };
      saveAuth(next);
      setAuth(next);
      setNameInput("");
      setEmailInput("");
      setPasswordInput("");
      setSignupTurnstileToken("");
      setSignupModalOpen(false);
      setError("");
    } catch (e) {
      setError((e as Error).message);
      if (signupNeedsTurnstile) setSignupTurnstileResetNonce((value) => value + 1);
      setSignupTurnstileToken("");
      setSignupModalOpen(signupNeedsTurnstile);
    } finally {
      setSignupBusy(false);
    }
  };

  const openSignUpVerification = () => {
    const trimmedName = nameInput.trim();
    if (!trimmedName) {
      setError("name_required");
      return;
    }
    if (Array.from(trimmedName).length > ACCOUNT_USERNAME_LIMIT) {
      setError("name_too_long");
      return;
    }
    const normalizedEmail = normalizeEmailInput(emailInput);
    if (!normalizedEmail) {
      setError("email_required");
      return;
    }
    if (!isValidEmailSyntax(normalizedEmail)) {
      setError("invalid_email");
      return;
    }
    if (passwordInput.length < ACCOUNT_PASSWORD_MIN_BYTES) {
      setError("password_too_short");
      return;
    }
    if (passwordInput.length > ACCOUNT_PASSWORD_MAX_BYTES) {
      setError("password_too_long");
      return;
    }

    setError("");
    setSignupTurnstileToken("");
    if (signupNeedsTurnstile) {
      setSignupTurnstileResetNonce((value) => value + 1);
      setSignupModalOpen(true);
      return;
    }
    void submitSignUp("");
  };

  const signIn = async () => {
    try {
      const data = await api<{ account_id: string; session_token: string }>("/accounts/login", null, {
        method: "POST",
        body: JSON.stringify({ name: loginNameInput, password: loginPasswordInput }),
      });
      const next: AccountAuth = { mode: "account", token: data.session_token };
      saveAuth(next);
      setAuth(next);
      setLoginPasswordInput("");
      setError("");
    } catch (e) {
      setError((e as Error).message);
    }
  };

  const createKey = async () => {
    if (!auth || auth.mode !== "account") return;
    try {
      await api<{ id: string; api_key: string }>("/account/api-keys", auth, {
        method: "POST",
        body: JSON.stringify({}),
      });
      await loadAccountData();
    } catch (e) {
      setError((e as Error).message);
    }
  };

  const saveResponderDescription = async () => {
    if (!auth || auth.mode !== "account") return;
    try {
      await api("/account/me", auth, {
        method: "PATCH",
        body: JSON.stringify({ responder_description: responderDescription }),
      });
      setError("");
    } catch (e) {
      setError((e as Error).message);
    }
  };

  const deleteKey = async (id: string) => {
    if (!auth || auth.mode !== "account") return;
    try {
      await api(`/account/api-keys/${id}`, auth, { method: "DELETE" });
      await loadAccountData();
    } catch (e) {
      setError((e as Error).message);
    }
  };

  const signOut = async () => {
    try {
      if (auth?.mode === "account") {
        await api("/account/logout", auth, { method: "POST" });
      }
    } catch {
      // Best-effort revoke. Local sign-out still needs to proceed.
    } finally {
      localStorage.removeItem(AUTH_KEY);
      window.location.reload();
    }
  };

  if (isSignedOut) {
    return (
      <main className="account-layout guest">
        <section className="account-guest-card">
          <h2 className="account-title">welcome to clawgrid</h2>
          <p className="account-subtext">an account is required to use clawgrid.</p>
          <div className="account-guest-auth-grid">
            <div className="account-guest-form">
              <p className="account-panel-label">sign in</p>
              <input
                className="account-description"
                value={loginNameInput}
                onChange={(e) => setLoginNameInput(e.target.value)}
                placeholder="username"
              />
              <input
                className="account-description"
                type="password"
                value={loginPasswordInput}
                onChange={(e) => setLoginPasswordInput(e.target.value)}
                placeholder="password"
              />
              <button className="account-btn primary" onClick={() => void signIn()}>
                sign in
              </button>
            </div>

            <div className="account-guest-form">
              <p className="account-panel-label">create account</p>
              <input
                className="account-description"
                value={nameInput}
                onChange={(e) => setNameInput(e.target.value)}
                placeholder="unique username"
              />
              <input
                className="account-description"
                value={emailInput}
                onChange={(e) => setEmailInput(e.target.value)}
                placeholder="email"
              />
              <input
                className="account-description"
                type="password"
                value={passwordInput}
                onChange={(e) => setPasswordInput(e.target.value)}
                placeholder="password"
              />
              <button className="account-btn" onClick={() => void openSignUpVerification()} disabled={signupBusy}>
                sign up
              </button>
            </div>
          </div>

          {signupModalOpen && (
            <div
              className="account-modal-backdrop"
              role="presentation"
              onClick={() => {
                if (signupBusy) return;
                setSignupModalOpen(false);
                setSignupTurnstileToken("");
                setSignupTurnstileResetNonce((value) => value + 1);
              }}
            >
              <div className="account-modal" role="dialog" aria-modal="true" aria-label="Complete verification" onClick={(e) => e.stopPropagation()}>
                <div className="account-modal-titlebar">
                  <p className="account-modal-title">create account</p>
                  <button
                    className="account-btn small"
                    onClick={() => {
                      setSignupModalOpen(false);
                      setSignupTurnstileToken("");
                      setSignupTurnstileResetNonce((value) => value + 1);
                    }}
                    disabled={signupBusy}
                  >
                    x
                  </button>
                </div>
                <div className="account-modal-body">
                  <p className="account-panel-label">complete verification</p>
                  <p className="account-muted">finish Turnstile verification to create your account.</p>
                  {!signupTurnstileToken && !signupBusy && (
                    <TurnstileWidget
                      sitekey={TURNSTILE_SITEKEY}
                      resetNonce={signupTurnstileResetNonce}
                      onTokenChange={handleSignupTurnstileTokenChange}
                      onError={handleSignupTurnstileError}
                    />
                  )}
                  {(signupTurnstileToken || signupBusy) && <p className="account-modal-status">creating account...</p>}
                </div>
                <div className="account-modal-actions">
                  <button
                    className="account-btn small"
                    onClick={() => {
                      setSignupModalOpen(false);
                      setSignupTurnstileToken("");
                      setSignupTurnstileResetNonce((value) => value + 1);
                    }}
                    disabled={signupBusy}
                  >
                    cancel
                  </button>
                </div>
                {error && <p className="inline-error">{error}</p>}
              </div>
            </div>
          )}

          {error && <p className="inline-error">{error}</p>}
        </section>
      </main>
    );
  }

  return (
    <main className="account-layout">
      <div className="account-header-row">
        <div>
          <h2 className="account-title">account overview: {accountName}</h2>
          <p className="account-muted">account id: {accountID}</p>
          {accountEmail && <p className="account-muted">email: {accountEmail}</p>}
        </div>
        <button
          className="account-btn small"
          onClick={() => void signOut()}
        >
          sign out
        </button>
      </div>

      <section className="account-balance-row">
        <article className="account-panel">
          <p className="account-panel-label">wallet balance</p>
          <p className="account-balance-value">{wallet ? `${wallet.balance.toFixed(2)} credits` : "-"}</p>
          <p className="account-muted">registered refill tier: up to 25 every 5 hours</p>
        </article>

        <article className="account-panel">
          <p className="account-panel-label">responder card blurb</p>
          <textarea
            className="account-description"
            rows={3}
            value={responderDescription}
            maxLength={420}
            onChange={(e) => setResponderDescription(e.target.value)}
          />
          <p className="account-counter">{responderDescriptionChars} / 420 chars</p>
          <button className="account-btn small" onClick={() => void saveResponderDescription()}>save blurb</button>
          <p className="account-muted">shown in dispatcher responder cards while this account is polling for jobs.</p>
        </article>
      </section>

      <section className="account-panel">
        <p className="account-panel-label">performance statistics</p>
        <div className="account-stats-grid">
          <div className="account-stat-card"><p className="account-stat-label">job success rate</p><p className="account-stat-value">{stats?.job_success_rate ?? "n/a"}</p></div>
          <div className="account-stat-card"><p className="account-stat-label">feedback rate</p><p className="account-stat-value">{stats?.feedback_rate ?? "n/a"}</p></div>
          <div className="account-stat-card"><p className="account-stat-label">dispatch accuracy</p><p className="account-stat-value">{stats?.dispatch_accuracy ?? "n/a"}</p></div>
          <div className="account-stat-card"><p className="account-stat-label">jobs completed</p><p className="account-stat-value">{stats ? String(stats.jobs_completed) : "n/a"}</p></div>
          <div className="account-stat-card"><p className="account-stat-label">jobs dispatched</p><p className="account-stat-value">{stats ? String(stats.jobs_dispatched) : "n/a"}</p></div>
        </div>
      </section>

      <section className="account-panel">
        <div className="account-api-head">
          <p className="account-panel-label">api access keys</p>
          <button className="account-btn small" onClick={() => void createKey()}>create api key</button>
        </div>

        <div className="account-keys-list">
          {keys.map((key) => (
            <div className="account-key-row" key={key.id}>
              <div className="account-key-main">
                <p className="account-key-id">{key.id}</p>
                <p className="account-key-meta">created {fmtTime(key.created_at)} | last used {fmtTime(key.last_used_at)}</p>
              </div>
              <div className="account-key-actions">
                <button className="account-btn small" onClick={() => navigator.clipboard.writeText(key.id)}>copy</button>
                <button className="account-btn small danger" onClick={() => void deleteKey(key.id)}>delete</button>
              </div>
            </div>
          ))}
        </div>
        <p className="account-muted">each listed key can be used directly as `Authorization: Bearer &lt;key&gt;`.</p>
      </section>
      {error && <p className="inline-error">{error}</p>}
    </main>
  );
}

function App() {
  const [activePage, setActivePage] = useState<Page>("account");
  const [auth, setAuthState] = useState<AuthState | null>(null);
  const [booting, setBooting] = useState(true);

  useEffect(() => {
    const bootstrap = async () => {
      const existing = loadAuth();
      if (existing) {
        try {
          const me = await api<{ auth_credential_type?: string }>("/account/me", existing);
          if (me.auth_credential_type === "api_key") {
            saveAuth(null);
          } else {
            setAuthState(existing);
            setBooting(false);
            return;
          }
        } catch {
          saveAuth(null);
        }
      }
      setAuthState(null);
      setBooting(false);
    };
    void bootstrap();
  }, []);

  const setAuth = (next: AuthState | null) => {
    saveAuth(next);
    setAuthState(next);
  };

  const renderPage = () => {
    if (booting) return <main className="placeholder-layout"><div className="placeholder-card">booting...</div></main>;
    if (!auth && activePage !== "leaderboard" && activePage !== "account") return <AccountPage auth={null} setAuth={setAuth} />;
    if (activePage === "ask") return <AskPage auth={auth} />;
    if (activePage === "dispatch") return <DispatchPage auth={auth} />;
    if (activePage === "respond") return <RespondPage auth={auth} />;
    if (activePage === "leaderboard") return <LeaderboardPage />;
    return <AccountPage auth={auth} setAuth={setAuth} />;
  };

  return (
    <div className="page-shell">
      <div className="panel">
        <header className="topbar">
          <h1 className="brand">clawgrid</h1>
          <nav className="nav-tabs" aria-label="Primary">
            <button className={`tab ${activePage === "ask" ? "active" : ""}`} onClick={() => setActivePage("ask")}>Ask</button>
            <button className={`tab ${activePage === "dispatch" ? "active" : ""}`} onClick={() => setActivePage("dispatch")}>Dispatch</button>
            <button className={`tab ${activePage === "respond" ? "active" : ""}`} onClick={() => setActivePage("respond")}>Respond</button>
            <button className={`tab ${activePage === "leaderboard" ? "active" : ""}`} onClick={() => setActivePage("leaderboard")}>Leaderboard</button>
            <button className={`tab ${activePage === "account" ? "active" : ""}`} onClick={() => setActivePage("account")}>Account</button>
          </nav>
        </header>
        <div className="page-content-fade" key={activePage}>{renderPage()}</div>
      </div>
    </div>
  );
}

export default App;

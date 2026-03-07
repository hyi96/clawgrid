import { useState } from "react";

type ChatRow = {
  id: number;
  text: string;
};

const chatRows: ChatRow[] = [
  { id: 1, text: "user: a message" },
  { id: 2, text: "responder_id: response response response response" },
  { id: 3, text: "user rated response as satisfactory" },
  { id: 4, text: "user: a new message continuing the conversation" },
  { id: 5, text: "responder_id: response response response response" },
  { id: 6, text: "user rated response as unsatisfactory" },
  { id: 7, text: "user: a new message continuing the conversation" },
  { id: 8, text: "responder_id: response response response response" },
  { id: 9, text: "user rated response as satisfactory" },
];

const sessionItems = ["session", "session", "session", "session", "session"];
const jobSlots = Array.from({ length: 6 });
const responderSlots = Array.from({ length: 5 });
const poolJobs = Array.from({ length: 6 });
const leaderboardCategories = [
  "job success rate",
  "dispatch accuracy",
  "total available credits",
] as const;
const leaderboardRows = Array.from({ length: 8 }).map((_, index) => ({ rank: index + 1, name: `account_${index + 1}` }));
const accountStats = [
  { label: "job success rate", value: "86.7%" },
  { label: "feedback rate", value: "42 / 50" },
  { label: "dispatch accuracy", value: "73.1%" },
  { label: "jobs completed", value: "128" },
  { label: "jobs dispatched", value: "89" },
  { label: "jobs responded", value: "176" },
];
const mockApiKeys = [
  { id: "ck_live_6v8a...91f2", createdAt: "2026-02-27", lastUsed: "2m ago", status: "active" },
  { id: "ck_live_3p2t...77c1", createdAt: "2026-01-11", lastUsed: "1d ago", status: "active" },
];
const respondLines: ChatRow[] = [
  { id: 1, text: "user: a message" },
  { id: 2, text: "responder_id: response response response response" },
  { id: 3, text: "user rated response as satisfactory" },
  { id: 4, text: "user: a new message continuing the conversation" },
  { id: 5, text: "responder_id: response response response response" },
  { id: 6, text: "user rated response as unsatisfactory" },
  { id: 7, text: "user: a new message continuing the conversation" },
];

type Page = "ask" | "dispatch" | "respond" | "leaderboard" | "account";
type RespondState = "poll" | "pool" | "active";

function AskPage() {
  return (
    <main className="ask-layout">
      <aside className="session-rail" aria-label="Sessions">
        <button className="new-session">new session</button>

        <div className="session-list">
          {sessionItems.map((item, index) => (
            <button className="session-item" key={`${item}-${index}`}>
              <span>{item}</span>
              <span className="session-more" aria-hidden="true">
                ...
              </span>
            </button>
          ))}
        </div>

        <p className="session-more-label">and more...</p>
      </aside>

      <section className="thread-panel" aria-label="Conversation">
        <div className="thread-content">
          {chatRows.map((row) => (
            <p key={row.id} className="thread-line">
              {row.text}
            </p>
          ))}
        </div>

        <div className="feedback-row" aria-label="Feedback actions">
          <button className="feedback-btn">thumbs up</button>
          <button className="feedback-btn">thumbs down</button>
        </div>
      </section>
    </main>
  );
}

function DispatchPage() {
  return (
    <main className="dispatch-layout">
      <section className="dispatch-section">
        <h2 className="dispatch-title">Jobs</h2>
        <div className="jobs-grid">
          {jobSlots.map((_, index) => (
            <div className="placeholder-box" key={`job-${index}`} />
          ))}
        </div>
      </section>

      <section className="dispatch-section">
        <h2 className="dispatch-title">Responders</h2>
        <div className="responders-grid">
          {responderSlots.map((_, index) => (
            <div className="placeholder-box" key={`responder-${index}`} />
          ))}
        </div>
      </section>
    </main>
  );
}

function AccountPage() {
  const [isGuest, setIsGuest] = useState(true);

  if (isGuest) {
    return (
      <main className="account-layout guest">
        <section className="account-guest-card">
          <h2 className="account-title">welcome to clawgrid</h2>
          <p className="account-subtext">sign in or create an account to unlock api keys and higher limits.</p>
          <div className="account-guest-actions">
            <button className="account-btn primary" onClick={() => setIsGuest(false)}>
              sign in
            </button>
            <button className="account-btn" onClick={() => setIsGuest(false)}>
              sign up
            </button>
          </div>
        </section>
      </main>
    );
  }

  return (
    <main className="account-layout">
      <div className="account-header-row">
        <h2 className="account-title">account overview</h2>
        <button className="account-btn small" onClick={() => setIsGuest(true)}>
          sign out (mock)
        </button>
      </div>

      <section className="account-balance-row">
        <article className="account-panel">
          <p className="account-panel-label">wallet balance</p>
          <p className="account-balance-value">23.40 credits</p>
          <p className="account-muted">registered refill tier: up to 25 every 5 hours</p>
        </article>

        <article className="account-panel">
          <p className="account-panel-label">responder card blurb</p>
          <textarea
            className="account-description"
            rows={3}
            defaultValue="generalist agent account. strong with code triage, ui skeletons, and api-first workflows."
          />
          <p className="account-muted">shown in dispatcher responder cards while this account is polling for jobs.</p>
        </article>
      </section>

      <section className="account-panel">
        <p className="account-panel-label">performance statistics</p>
        <div className="account-stats-grid">
          {accountStats.map((item) => (
            <div className="account-stat-card" key={item.label}>
              <p className="account-stat-label">{item.label}</p>
              <p className="account-stat-value">{item.value}</p>
            </div>
          ))}
        </div>
      </section>

      <section className="account-panel">
        <div className="account-api-head">
          <p className="account-panel-label">api key management</p>
          <button className="account-btn small">create new key</button>
        </div>

        <div className="account-keys-list">
          {mockApiKeys.map((key) => (
            <div className="account-key-row" key={key.id}>
              <div className="account-key-main">
                <p className="account-key-id">{key.id}</p>
                <p className="account-key-meta">
                  created {key.createdAt} | last used {key.lastUsed} | {key.status}
                </p>
              </div>
              <div className="account-key-actions">
                <button className="account-btn small">copy</button>
                <button className="account-btn small danger">delete</button>
              </div>
            </div>
          ))}
        </div>
      </section>
    </main>
  );
}

function LeaderboardPage() {
  const [activeBoard, setActiveBoard] = useState<(typeof leaderboardCategories)[number]>(
    leaderboardCategories[0],
  );

  const getMetricValue = (rank: number) => {
    if (activeBoard === "total available credits") return `${(325 - rank * 17).toFixed(1)} credits`;
    if (activeBoard === "dispatch accuracy") return `${(86 - rank * 2.1).toFixed(1)}%`;
    return `${(93 - rank * 1.8).toFixed(1)}%`;
  };

  const qualificationRule =
    activeBoard === "dispatch accuracy"
      ? "shown only for accounts with >=50 completed dispatches"
      : "shown only for accounts with >=50 completed jobs";

  return (
    <main className="leaderboard-layout">
      <div className="leaderboard-head">
        <h2 className="leaderboard-title">leaderboards</h2>
        <p className="leaderboard-refresh">refreshed daily at 00:00 utc</p>
      </div>

      <div className="leaderboard-tabs">
        {leaderboardCategories.map((category) => (
          <button
            className={`leaderboard-tab ${activeBoard === category ? "active" : ""}`}
            key={category}
            onClick={() => setActiveBoard(category)}
          >
            {category}
          </button>
        ))}
      </div>

      <section className="leaderboard-panel">
        <p className="leaderboard-qualification">{qualificationRule}</p>
        <div className="leaderboard-table-head">
          <span>rank</span>
          <span>account</span>
          <span>{activeBoard}</span>
        </div>

        <div className="leaderboard-table-body">
          {leaderboardRows.map((row) => (
            <div className="leaderboard-row" key={row.rank}>
              <span>#{row.rank}</span>
              <span>{row.name}</span>
              <span>{getMetricValue(row.rank)}</span>
            </div>
          ))}
        </div>
      </section>
    </main>
  );
}

function RespondPage() {
  const [respondState, setRespondState] = useState<RespondState>("poll");

  if (respondState === "poll") {
    return (
      <main className="respond-poll-layout">
        <div className="respond-poll-center">
          <button className="respond-main-btn" onClick={() => setRespondState("pool")}>
            Poll Job/s
          </button>
          <button className="respond-sub-btn" onClick={() => setRespondState("active")}>
            simulate assigned job
          </button>
        </div>
        <p className="respond-help-text">
          polling first checks assigned jobs. if none arrive during the wait window, system pool jobs are shown.
        </p>
      </main>
    );
  }

  if (respondState === "pool") {
    return (
      <main className="respond-pool-layout">
        <div className="respond-pool-head">
          <h2 className="respond-pool-title">Select job from the following</h2>
          <button className="respond-sub-btn" onClick={() => setRespondState("poll")}>
            back to poll
          </button>
        </div>

        <div className="respond-pool-grid">
          {poolJobs.map((_, index) => (
            <button className="respond-pool-card" key={`pool-${index}`} onClick={() => setRespondState("active")}>
              <span>session snippets and metadata</span>
              <span>last message: xxxx</span>
            </button>
          ))}
        </div>

        <p className="respond-help-text">
          system pool appears only after polling yields no direct assignment. selecting a card opens response mode.
        </p>
      </main>
    );
  }

  return (
    <main className="respond-active-layout">
      <aside className="respond-side">
        <div className="respond-side-box">timer</div>
        <div className="respond-side-box">other stuff about the job. metadata, etc.</div>
        <div className="respond-side-box">attachments for this job</div>
      </aside>

      <section className="respond-thread-panel">
        <div className="respond-thread-content">
          {respondLines.map((row) => (
            <p key={row.id} className="respond-thread-line">
              {row.text}
            </p>
          ))}
        </div>

        <div className="respond-composer">
          <button className="respond-attach-btn" aria-label="Add attachment">
            +
          </button>
          <input
            className="respond-composer-input"
            type="text"
            placeholder="enter text here and enter to send"
            readOnly
          />
        </div>
      </section>
    </main>
  );
}

function App() {
  const [activePage, setActivePage] = useState<Page>("ask");

  const renderPage = () => {
    if (activePage === "ask") return <AskPage />;
    if (activePage === "dispatch") return <DispatchPage />;
    if (activePage === "respond") return <RespondPage />;
    if (activePage === "leaderboard") return <LeaderboardPage />;
    return <AccountPage />;
  };

  return (
    <div className="page-shell">
      <div className="panel">
        <header className="topbar">
          <h1 className="brand">clawgrid</h1>
          <nav className="nav-tabs" aria-label="Primary">
            <button
              className={`tab ${activePage === "ask" ? "active" : ""}`}
              onClick={() => setActivePage("ask")}
            >
              Ask
            </button>
            <button
              className={`tab ${activePage === "dispatch" ? "active" : ""}`}
              onClick={() => setActivePage("dispatch")}
            >
              Dispatch
            </button>
            <button
              className={`tab ${activePage === "respond" ? "active" : ""}`}
              onClick={() => setActivePage("respond")}
            >
              Respond
            </button>
            <button
              className={`tab ${activePage === "leaderboard" ? "active" : ""}`}
              onClick={() => setActivePage("leaderboard")}
            >
              Leaderboard
            </button>
            <button
              className={`tab ${activePage === "account" ? "active" : ""}`}
              onClick={() => setActivePage("account")}
            >
              Account
            </button>
          </nav>
        </header>
        {renderPage()}
      </div>
    </div>
  );
}

export default App;

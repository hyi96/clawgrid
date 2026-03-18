import { useEffect, useRef } from "react";

const TURNSTILE_SCRIPT_ID = "cloudflare-turnstile-script";
const TURNSTILE_SCRIPT_SRC = "https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit";

function loadTurnstileScript(): Promise<void> {
  if (window.turnstile) return Promise.resolve();
  const existing = document.getElementById(TURNSTILE_SCRIPT_ID) as HTMLScriptElement | null;
  if (existing) {
    return new Promise((resolve, reject) => {
      existing.addEventListener("load", () => resolve(), { once: true });
      existing.addEventListener("error", () => reject(new Error("turnstile_script_failed")), { once: true });
    });
  }
  return new Promise((resolve, reject) => {
    const script = document.createElement("script");
    script.id = TURNSTILE_SCRIPT_ID;
    script.src = TURNSTILE_SCRIPT_SRC;
    script.async = true;
    script.defer = true;
    script.onload = () => resolve();
    script.onerror = () => reject(new Error("turnstile_script_failed"));
    document.head.appendChild(script);
  });
}

type Props = {
  sitekey: string;
  resetNonce: number;
  onTokenChange: (token: string) => void;
  onError: (message: string) => void;
};

export function TurnstileWidget({ sitekey, resetNonce, onTokenChange, onError }: Props) {
  const containerRef = useRef<HTMLDivElement | null>(null);
  const widgetIDRef = useRef<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    if (!sitekey || !containerRef.current) return undefined;

    void loadTurnstileScript()
      .then(() => {
        if (cancelled || !containerRef.current || !window.turnstile || widgetIDRef.current) return;
        widgetIDRef.current = window.turnstile.render(containerRef.current, {
          sitekey,
          theme: "dark",
          callback: (token) => onTokenChange(token),
          "expired-callback": () => onTokenChange(""),
          "error-callback": () => {
            onTokenChange("");
            onError("turnstile_failed");
          },
        });
      })
      .catch(() => {
        if (!cancelled) onError("turnstile_failed");
      });

    return () => {
      cancelled = true;
      if (widgetIDRef.current && window.turnstile) {
        window.turnstile.remove(widgetIDRef.current);
      }
      widgetIDRef.current = null;
    };
  }, [sitekey, onError, onTokenChange]);

  useEffect(() => {
    if (!widgetIDRef.current || !window.turnstile) return;
    window.turnstile.reset(widgetIDRef.current);
    onTokenChange("");
  }, [resetNonce, onTokenChange]);

  return <div className="turnstile-widget" ref={containerRef} />;
}

import { useCallback, useEffect, useRef, useState } from "react";
import { api, ApiError, describeError, setUnauthorizedHandler } from "./api";
import { Library } from "./Library";
import { Workbench } from "./Workbench";

interface Session {
  authenticated: boolean;
  model_configured: boolean;
}

function Login({ onLogin, expired }: { onLogin: (s: Session) => void; expired: boolean }) {
  const [pw, setPw] = useState("");
  const [err, setErr] = useState(expired ? "登录已失效，请重新登录；未保存的内容仍保留在页面里。" : "");
  const [busy, setBusy] = useState(false);
  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      const s = await api<Session>("POST", "/api/session", { password: pw });
      setPw("");
      onLogin(s);
    } catch (e2) {
      setErr(e2 instanceof ApiError && e2.status === 401 ? "密码不正确。" : describeError(e2));
    } finally {
      setBusy(false);
    }
  };
  return (
    <main className="login">
      <form className="panel form" onSubmit={submit} aria-label="登录">
        <h1>Prompt 库</h1>
        <div className="field">
          <label htmlFor="pw">站点密码</label>
          <input id="pw" type="password" autoComplete="current-password" autoFocus value={pw} onChange={(e) => setPw(e.target.value)} />
        </div>
        <p className="error" role="alert">
          {err}
        </p>
        <button type="submit" className="primary" disabled={busy || !pw}>
          登录
        </button>
      </form>
    </main>
  );
}

export function App() {
  const [session, setSession] = useState<Session | null>(null);
  const [startErr, setStartErr] = useState("");
  const [everIn, setEverIn] = useState(false);
  const [tab, setTab] = useState<"library" | "workbench">("library");
  const [incoming, setIncoming] = useState<{ text: string; n: number } | null>(null);
  const [version, setVersion] = useState(0);
  const [authGen, setAuthGen] = useState(0);
  const [focusId, setFocusId] = useState<number | null>(null);
  const n = useRef(0);

  useEffect(() => {
    api<Session>("GET", "/api/session")
      .then(setSession)
      .catch((e) => setStartErr(describeError(e)));
    setUnauthorizedHandler(() => setSession((s) => (s ? { ...s, authenticated: false } : s)));
  }, []);

  const authed = session?.authenticated ?? false;
  useEffect(() => {
    if (authed) setEverIn(true);
  }, [authed]);

  const logout = async () => {
    try {
      await api("DELETE", "/api/session");
    } finally {
      setEverIn(false);
      setSession((s) => (s ? { ...s, authenticated: false } : s));
    }
  };

  const toWorkbench = useCallback((text: string) => {
    setIncoming({ text, n: ++n.current });
    setTab("workbench");
  }, []);

  if (startErr) return <p className="error page-msg" role="alert">{startErr}</p>;
  if (!session) return <p className="muted page-msg">加载中…</p>;

  return (
    <>
      {!authed && (
        <Login
          onLogin={(s) => {
            setAuthGen((g) => g + 1);
            setSession(s);
          }}
          expired={everIn}
        />
      )}
      {(authed || everIn) && (
        <div className="shell" hidden={!authed}>
          <nav className="side" aria-label="主导航">
            <h1>Prompt 库</h1>
            <button type="button" aria-current={tab === "library"} onClick={() => setTab("library")}>
              Prompt 库
            </button>
            <button type="button" aria-current={tab === "workbench"} onClick={() => setTab("workbench")}>
              优化工作台
            </button>
            <button type="button" className="logout" onClick={logout}>
              退出登录
            </button>
          </nav>
          {/* Both pages stay mounted so unsaved forms and in-flight work survive tab switches. */}
          <main>
            <div hidden={tab !== "library"}>
              <Library
                version={version}
                authGen={authGen}
                focusId={focusId}
                modelConfigured={session.model_configured}
                onLoadToWorkbench={toWorkbench}
              />
            </div>
            <div hidden={tab !== "workbench"}>
              <Workbench
                modelConfigured={session.model_configured}
                active={tab === "workbench"}
                authGen={authGen}
                incoming={incoming}
                onSaved={(p) => {
                  setVersion((v) => v + 1);
                  setFocusId(p.id);
                }}
              />
            </div>
          </main>
        </div>
      )}
    </>
  );
}

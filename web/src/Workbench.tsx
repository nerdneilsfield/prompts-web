import { useEffect, useMemo, useRef, useState } from "react";
import {
  api,
  apiRaw,
  ApiError,
  describeError,
  newRequestId,
  runErrorText,
  type Prompt,
  type Run,
  type Scheme,
} from "./api";
import { Schemes } from "./Schemes";
import { TagInput, addTag } from "./TagInput";
import { charCount, charDiff, copyText, placeholderDelta } from "./text";

interface Props {
  modelConfigured: boolean;
  active: boolean;
  authGen: number;
  incoming: { text: string; n: number } | null;
  onSaved: (p: Prompt) => void;
}

function DiffView({ before, after }: { before: string; after: string }) {
  const pieces = useMemo(() => charDiff(before, after), [before, after]);
  if (pieces === null) return <p className="muted">文本过长，已省略字符差异。</p>;
  if (before === after) return <p className="muted">结果与原文完全相同。</p>;
  return (
    <pre className="diff" data-testid="diff">
      {pieces.map((p, i) =>
        p.kind === "same" ? (
          <span key={i}>{p.text}</span>
        ) : p.kind === "add" ? (
          <ins key={i}>{p.text}</ins>
        ) : (
          <del key={i}>{p.text}</del>
        ),
      )}
    </pre>
  );
}

function SaveAs({ run, text, onSaved }: { run: Run; text: string; onSaved: (p: Prompt) => void }) {
  const [title, setTitle] = useState("");
  const [tags, setTags] = useState<string[]>([]);
  const [draft, setDraft] = useState("");
  const [err, setErr] = useState("");
  const [saved, setSaved] = useState<Prompt | null>(null);
  const [busy, setBusy] = useState(false);
  const lock = useRef(false);

  const save = async (e: React.FormEvent) => {
    e.preventDefault();
    if (lock.current) return;
    if (!title.trim() || !text.trim()) {
      setErr("另存需要标题，且结果正文不能为空。");
      return;
    }
    lock.current = true;
    setBusy(true);
    setErr("");
    try {
      const p = await api<Prompt>("POST", "/api/prompts", {
        title: title.trim(),
        body: text,
        tags: addTag(tags, draft),
        source_run_id: run.request_id,
      });
      setSaved(p);
      setTitle("");
      setTags([]);
      setDraft("");
      onSaved(p);
    } catch (e2) {
      setErr(describeError(e2));
    } finally {
      lock.current = false;
      setBusy(false);
    }
  };

  return (
    <form className="panel form" onSubmit={save} aria-label="另存为新 Prompt">
      <h3>另存为新 Prompt</h3>
      <p className="muted small">只会新增一条，不会改动原来的 Prompt；会记录本次运行作为出处。</p>
      <div className="field">
        <label htmlFor="sa-title">标题</label>
        <input id="sa-title" value={title} onChange={(e) => setTitle(e.target.value)} />
      </div>
      <TagInput tags={tags} draft={draft} onTags={setTags} onDraft={setDraft} />
      <p className="error" role="alert">
        {err}
      </p>
      <div className="row">
        <button type="submit" className="primary" disabled={busy} aria-busy={busy}>
          另存
        </button>
      </div>
      {saved && (
        <p role="status" className="ok">
          已另存为「{saved.title}」，可在 Prompt 库查看。
        </p>
      )}
    </form>
  );
}

export function Workbench({ modelConfigured, active, authGen, incoming, onSaved }: Props) {
  const [input, setInput] = useState("");
  const [schemes, setSchemes] = useState<Scheme[] | null>(null);
  const [schemeId, setSchemeId] = useState<number | null>(null);
  const [loadErr, setLoadErr] = useState("");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [run, setRun] = useState<Run | null>(null);
  const [edited, setEdited] = useState("");
  const [notice, setNotice] = useState("");
  // A POST whose outcome we do not know yet. Its ID stays until a terminal run or a 404 proves the
  // server has nothing, so a retry can never mint a second ID for work that may already be billed.
  const [pending, setPending] = useState<string | null>(null);
  const [looking, setLooking] = useState(false);
  const pendingRef = useRef<string | null>(null);
  const setPend = (id: string | null) => {
    pendingRef.current = id;
    setPending(id);
  };

  // Every input/scheme change bumps `epoch`; a response is applied only if the epoch it was
  // sent under is still current. `inflight` is the synchronous double-click lock.
  const epoch = useRef(0);
  const inflight = useRef(false);
  const abort = useRef<AbortController | null>(null);

  const invalidate = () => {
    epoch.current++;
    abort.current?.abort();
    abort.current = null;
    inflight.current = false;
    setLoading(false);
    setLooking(false);
    setError("");
  };
  // The input or scheme changed (or was replaced): the pending request no longer describes this work.
  const reset = () => {
    invalidate();
    setPend(null);
  };

  const loadSchemes = async (removedOrEdited?: number) => {
    try {
      const list = await api<Scheme[]>("GET", "/api/schemes");
      setSchemes(list);
      setLoadErr("");
      setSchemeId((cur) => (cur !== null && list.some((s) => s.id === cur) ? cur : (list[0]?.id ?? null)));
      if (removedOrEdited !== undefined) reset();
    } catch (e) {
      setLoadErr(describeError(e));
    }
  };
  // Re-run after a re-login so a load that failed with 401 recovers; the draft stays.
  useEffect(() => {
    void loadSchemes();
  }, [authGen]);
  useEffect(() => () => abort.current?.abort(), []);

  // Leaving the page cancels in-flight work and drops its result; the draft stays.
  useEffect(() => {
    if (!active) invalidate();
  }, [active]);

  useEffect(() => {
    if (!incoming) return;
    if (input.trim() && input !== incoming.text && !window.confirm("工作台里已有未处理的原文，确定用所选 Prompt 替换吗？")) return;
    reset();
    setInput(incoming.text);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [incoming]);

  const applyRun = (r: Run) => {
    setRun(r);
    setEdited(r.result);
    setError(r.status === "succeeded" ? "" : (runErrorText[r.error_kind ?? ""] ?? "优化失败，请重试。"));
  };

  // One GET by ID, never polled. Terminal -> show it and release the ID; 404 -> the server has
  // nothing, release; running or unreachable -> keep the ID and say so.
  const lookupOnce = async (id: string, current: () => boolean, signal: AbortSignal) => {
    try {
      const r = await api<Run>("GET", `/api/optimizations/${id}`, undefined, signal);
      if (!current()) return;
      if (r.status === "running") {
        setError("服务端仍在处理这次请求。点「查询上次请求」再看一次；不会重复提交，也不会产生第二次调用。");
      } else {
        setPend(null);
        applyRun(r);
      }
    } catch (e) {
      if (!current() || signal.aborted) return;
      if (e instanceof ApiError && e.status === 404) {
        setPend(null);
        setError("服务端没有这次请求的记录，可以重新开始优化。输入已保留。");
      } else {
        setError("暂时无法确认上次请求的状态。已保留该请求，可再次点「查询上次请求」；输入已保留。");
      }
    }
  };

  const optimize = async () => {
    if (inflight.current || pendingRef.current || schemeId === null || !input.trim()) return;
    inflight.current = true;
    const myEpoch = epoch.current;
    const ac = new AbortController();
    abort.current = ac;
    const requestId = newRequestId();
    setPend(requestId);
    setLoading(true);
    setError("");
    setNotice("");
    const current = () => epoch.current === myEpoch;
    try {
      const { status, data } = await apiRaw<Run>(
        "POST",
        "/api/optimizations",
        { request_id: requestId, input, scheme_id: schemeId },
        ac.signal,
      );
      if (!current()) return;
      if (status === 202 || data.status === "running") await lookupOnce(requestId, current, ac.signal);
      else {
        setPend(null);
        applyRun(data);
      }
    } catch (e) {
      if (!current() || ac.signal.aborted) return;
      if (e instanceof ApiError && e.status < 500) {
        // The server rejected it before running anything: nothing to recover.
        setPend(null);
        setError(e.code === "model_not_configured" ? "服务端未配置模型，无法优化。" : describeError(e));
      } else if (e instanceof ApiError) {
        // 5xx: the run may exist (e.g. its final state could not be stored). Keep the ID.
        setError(`${describeError(e)}这次请求可能已在服务端运行，可点「查询上次请求」确认，输入已保留。`);
      } else {
        // The response may have been lost after the server ran it: look once, keep the ID.
        await lookupOnce(requestId, current, ac.signal);
      }
    } finally {
      if (current()) {
        inflight.current = false;
        abort.current = null;
        setLoading(false);
      }
    }
  };

  const lookupPending = async () => {
    const id = pendingRef.current;
    if (!id || inflight.current) return;
    inflight.current = true;
    const myEpoch = epoch.current;
    const ac = new AbortController();
    abort.current = ac;
    const current = () => epoch.current === myEpoch;
    setLooking(true);
    setError("");
    try {
      await lookupOnce(id, current, ac.signal);
    } finally {
      if (current()) {
        inflight.current = false;
        abort.current = null;
        setLooking(false);
      }
    }
  };

  const delta = run ? placeholderDelta(run.input, edited) : null;
  const canRun = modelConfigured && schemeId !== null && input.trim() !== "" && !loading && !looking && !pending;

  return (
    <div className="workbench">
      {!modelConfigured && (
        <p className="banner" role="status">
          服务端未配置模型（LLM_BASE_URL / LLM_API_KEY / LLM_MODEL），暂时不能优化。方案仍可管理，Prompt 库不受影响。
        </p>
      )}

      <div className="panel form">
        <div className="field">
          <label htmlFor="wb-input">原文</label>
          <textarea
            id="wb-input"
            rows={6}
            value={input}
            onChange={(e) => {
              reset();
              setInput(e.target.value);
            }}
            placeholder="粘贴要优化的 prompt，或在 Prompt 库中点「载入优化」"
          />
          <span className="muted small">{charCount(input)} 字符</span>
        </div>
        <div className="row wrap end">
          <div className="field grow">
            <label htmlFor="wb-scheme">优化方案</label>
            <select
              id="wb-scheme"
              value={schemeId ?? ""}
              disabled={!schemes?.length}
              onChange={(e) => {
                reset();
                setSchemeId(Number(e.target.value));
              }}
            >
              {schemes === null && <option>加载中…</option>}
              {schemes?.length === 0 && <option>暂无方案</option>}
              {schemes?.map((s) => (
                <option key={s.id} value={s.id}>
                  {s.name}
                </option>
              ))}
            </select>
          </div>
          <button type="button" className="primary" onClick={optimize} disabled={!canRun} aria-busy={loading}>
            {loading ? "优化中…（最长约 90 秒）" : error && !pending ? "重试优化" : "开始优化"}
          </button>
          {pending && (
            <button type="button" onClick={lookupPending} disabled={loading || looking} aria-busy={looking}>
              {looking ? "查询中…" : "查询上次请求"}
            </button>
          )}
        </div>
        <p className="error" role="alert">
          {error || loadErr}
        </p>
        {pending && !loading && <p className="muted small">上次请求结果未确认。修改原文或方案会放弃它。</p>}
        {notice && <p role="status">{notice}</p>}
      </div>

      {run && run.status === "succeeded" && delta && (
        <section aria-label="优化结果">
          <div className="compare">
            <div className="panel">
              <h3>原文</h3>
              <pre className="body" data-testid="orig">{run.input}</pre>
              <span className="muted small">{charCount(run.input)} 字符</span>
            </div>
            <div className="panel form">
              <div className="field">
                <label htmlFor="wb-result">优化结果（可编辑）</label>
                <textarea id="wb-result" rows={10} value={edited} onChange={(e) => setEdited(e.target.value)} />
              </div>
              <span className="muted small" data-testid="lengths">
                {charCount(edited)} 字符（{charCount(edited) - charCount(run.input) >= 0 ? "+" : ""}
                {charCount(edited) - charCount(run.input)}）· 方案「{run.scheme_name}」· {run.model}
              </span>
              <div className="row">
                <button
                  type="button"
                  onClick={async () => setNotice((await copyText(edited)) ? "已复制结果。" : "复制失败，请手动复制。")}
                >
                  复制结果
                </button>
                {edited !== run.result && (
                  <button type="button" onClick={() => setEdited(run.result)}>
                    恢复模型原始输出
                  </button>
                )}
              </div>
            </div>
          </div>

          {(delta.lost.length > 0 || delta.added.length > 0) && (
            <div className="warn" role="status" data-testid="placeholder-warning">
              {delta.lost.length > 0 && (
                <p>
                  丢失的变量：<code>{delta.lost.join("  ")}</code>
                </p>
              )}
              {delta.added.length > 0 && (
                <p>
                  新增的变量：<code>{delta.added.join("  ")}</code>
                </p>
              )}
            </div>
          )}

          <div className="panel">
            <h3>字符差异</h3>
            <DiffView before={run.input} after={edited} />
          </div>

          <SaveAs run={run} text={edited} onSaved={onSaved} />
        </section>
      )}

      <Schemes schemes={schemes ?? []} onChanged={loadSchemes} />
    </div>
  );
}

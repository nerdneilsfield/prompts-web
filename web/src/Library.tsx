import { useCallback, useEffect, useRef, useState } from "react";
import { api, describeError, type Prompt, type PromptInput, type Run } from "./api";
import { addTag, TagInput } from "./TagInput";
import { copyText, formatTime } from "./text";

type Mode = { kind: "view" } | { kind: "new" } | { kind: "edit" };

function useDebounced<T>(value: T, ms: number): T {
  const [v, setV] = useState(value);
  useEffect(() => {
    const t = setTimeout(() => setV(value), ms);
    return () => clearTimeout(t);
  }, [value, ms]);
  return v;
}

function TagChip({ tag, active, onPick }: { tag: string; active: boolean; onPick: (t: string) => void }) {
  return (
    <button
      type="button"
      className={`chip chip-pick${active ? " active" : ""}`}
      aria-pressed={active}
      onClick={(e) => {
        e.stopPropagation();
        onPick(tag);
      }}
    >
      <span className="chip-label" title={tag}>{tag}</span>
    </button>
  );
}

function SourceRun({ id }: { id: string }) {
  const [run, setRun] = useState<Run | null>(null);
  const [err, setErr] = useState("");
  const [open, setOpen] = useState(false);
  const toggle = async () => {
    setOpen(!open);
    if (open || run) return;
    try {
      setRun(await api<Run>("GET", `/api/optimizations/${encodeURIComponent(id)}`));
    } catch (e) {
      setErr(describeError(e));
    }
  };
  return (
    <div className="source">
      <button type="button" onClick={toggle} aria-expanded={open}>
        {open ? "收起来源" : "查看来源"}
      </button>
      {open && err && <p className="error">{err}</p>}
      {open && run && (
        <div className="source-body">
          <h4>原始输入</h4>
          <pre>{run.input}</pre>
          <h4>
            方案快照：{run.scheme_name}（模型 {run.model}）
          </h4>
          <pre>{run.scheme_snapshot}</pre>
          <h4>模型原始输出</h4>
          <pre>{run.result}</pre>
        </div>
      )}
    </div>
  );
}

interface FormProps {
  initial: { title: string; body: string; tags: string[] };
  heading: string;
  onSubmit: (v: PromptInput) => Promise<void>;
  onCancel: () => void;
  onDirty: (d: boolean) => void;
}

function PromptForm({ initial, heading, onSubmit, onCancel, onDirty }: FormProps) {
  const [title, setTitle] = useState(initial.title);
  const [body, setBody] = useState(initial.body);
  const [tags, setTags] = useState(initial.tags);
  const [draft, setDraft] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const lock = useRef(false);

  const dirty =
    title !== initial.title ||
    body !== initial.body ||
    draft.trim() !== "" ||
    tags.length !== initial.tags.length ||
    tags.some((t, i) => t !== initial.tags[i]);
  useEffect(() => {
    onDirty(dirty);
    return () => onDirty(false);
  }, [dirty, onDirty]);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (lock.current) return;
    if (!title.trim() || !body.trim()) {
      setErr("标题和正文都不能为空。");
      return;
    }
    lock.current = true;
    setBusy(true);
    setErr("");
    try {
      await onSubmit({ title: title.trim(), body, tags: addTag(tags, draft) });
    } catch (e2) {
      setErr(describeError(e2));
      lock.current = false;
      setBusy(false);
    }
  };

  return (
    <form className="panel form" onSubmit={submit} aria-label={heading}>
      <h2>{heading}</h2>
      <div className="field">
        <label htmlFor="pf-title">标题</label>
        <input id="pf-title" value={title} onChange={(e) => setTitle(e.target.value)} />
      </div>
      <div className="field">
        <label htmlFor="pf-body">正文</label>
        <textarea id="pf-body" rows={12} value={body} onChange={(e) => setBody(e.target.value)} />
      </div>
      <TagInput tags={tags} draft={draft} onTags={setTags} onDraft={setDraft} />
      <p className="error" role="alert">
        {err}
      </p>
      <div className="row">
        <button type="submit" className="primary" disabled={busy} aria-busy={busy}>
          {busy ? "保存中…" : "保存"}
        </button>
        <button type="button" onClick={onCancel} disabled={busy}>
          取消
        </button>
      </div>
    </form>
  );
}

interface Props {
  version: number;
  authGen: number;
  focusId: number | null;
  modelConfigured: boolean;
  onLoadToWorkbench: (body: string) => void;
}

export function Library({ version, authGen, focusId, modelConfigured, onLoadToWorkbench }: Props) {
  const [all, setAll] = useState<Prompt[] | null>(null);
  const [filtered, setFiltered] = useState<Prompt[] | null>(null);
  const [err, setErr] = useState("");
  const [qInput, setQInput] = useState("");
  const q = useDebounced(qInput.trim(), 250);
  const [tag, setTag] = useState("");
  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [mode, setMode] = useState<Mode>({ kind: "view" });
  const [notice, setNotice] = useState("");
  const [local, setLocal] = useState(0);
  const [pendingFocus, setPendingFocus] = useState<number | null>(null);
  const dirty = useRef(false);
  const setDirty = useCallback((d: boolean) => {
    dirty.current = d;
  }, []);
  const filtering = q !== "" || tag !== "";

  const reload = () => setLocal((n) => n + 1);

  useEffect(() => {
    const ac = new AbortController();
    api<Prompt[]>("GET", "/api/prompts", undefined, ac.signal)
      .then((d) => {
        setAll(d);
        setErr("");
      })
      .catch((e) => !ac.signal.aborted && setErr(describeError(e)));
    return () => ac.abort();
  }, [version, local, authGen]);

  useEffect(() => {
    if (!filtering) {
      setFiltered(null);
      return;
    }
    const ac = new AbortController();
    setFiltered(null);
    const params = new URLSearchParams();
    if (q) params.set("q", q);
    if (tag) params.set("tag", tag);
    api<Prompt[]>("GET", `/api/prompts?${params}`, undefined, ac.signal)
      .then((d) => {
        setFiltered(d);
        setErr("");
      })
      .catch((e) => !ac.signal.aborted && setErr(describeError(e)));
    return () => ac.abort();
  }, [q, tag, filtering, version, local, authGen]);

  // A new item saved from the workbench must not replace an open, unsaved form:
  // park it and show it once the form is closed.
  useEffect(() => {
    if (focusId === null) return;
    if (dirty.current) setPendingFocus(focusId);
    else {
      setSelectedId(focusId);
      setMode({ kind: "view" });
    }
  }, [focusId]);
  useEffect(() => {
    if (pendingFocus !== null && mode.kind === "view") {
      setSelectedId(pendingFocus);
      setPendingFocus(null);
    }
  }, [pendingFocus, mode.kind]);

  useEffect(() => {
    const h = (e: BeforeUnloadEvent) => {
      if (dirty.current) e.preventDefault();
    };
    window.addEventListener("beforeunload", h);
    return () => window.removeEventListener("beforeunload", h);
  }, []);

  const guard = () => !dirty.current || window.confirm("表单有未保存的修改，确定放弃吗？");

  const list = filtering ? filtered : all;
  const selected = all?.find((p) => p.id === selectedId) ?? null;

  const tagCounts = new Map<string, number>();
  all?.forEach((p) => p.tags.forEach((t) => tagCounts.set(t, (tagCounts.get(t) ?? 0) + 1)));
  const tagList = [...tagCounts].sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0], "zh"));

  const pickTag = (t: string) => setTag(tag === t ? "" : t);
  const select = (id: number) => {
    if (id === selectedId && mode.kind === "view") return;
    if (!guard()) return;
    setSelectedId(id);
    setMode({ kind: "view" });
    setNotice("");
  };
  const startNew = () => {
    if (!guard()) return;
    setMode({ kind: "new" });
    setNotice("");
  };
  const startEdit = () => {
    if (!guard()) return;
    setMode({ kind: "edit" });
    setNotice("");
  };
  const cancel = () => {
    if (!guard()) return;
    setMode({ kind: "view" });
  };

  const create = async (v: PromptInput) => {
    const p = await api<Prompt>("POST", "/api/prompts", v);
    setDirty(false);
    setAll((a) => [p, ...(a ?? [])]);
    setQInput("");
    setTag("");
    setSelectedId(p.id);
    setMode({ kind: "view" });
    setNotice("已新增。");
    reload();
  };
  const update = async (v: PromptInput) => {
    const p = await api<Prompt>("PUT", `/api/prompts/${selected!.id}`, v);
    setDirty(false);
    setAll((a) => a?.map((x) => (x.id === p.id ? p : x)) ?? a);
    setSelectedId(p.id);
    setMode({ kind: "view" });
    setNotice("已保存修改。");
    reload();
  };
  const remove = async () => {
    if (!selected || !window.confirm(`确定删除「${selected.title}」吗？此操作不可恢复。`)) return;
    try {
      await api("DELETE", `/api/prompts/${selected.id}`);
      setSelectedId(null);
      setNotice("已删除。");
      reload();
    } catch (e) {
      setNotice(describeError(e));
    }
  };
  const copy = async () => {
    if (!selected) return;
    setNotice((await copyText(selected.body)) ? "已复制正文。" : "复制失败，请手动选择文字复制。");
  };

  return (
    <div className="library">
      <section className="list-col" aria-label="Prompt 列表">
        <div className="row">
          <button type="button" className="primary" onClick={startNew}>
            ＋ 新增 Prompt
          </button>
        </div>
        <div className="field">
          <label htmlFor="search">搜索标题或正文</label>
          <input id="search" type="search" value={qInput} onChange={(e) => setQInput(e.target.value)} />
        </div>
        <div className="tagbar" aria-label="标签筛选">
          {tagList.length === 0 && <span className="muted small">还没有标签</span>}
          {tagList.map(([t, n]) => (
            <button
              key={t}
              type="button"
              className={`chip chip-pick${tag === t ? " active" : ""}`}
              aria-pressed={tag === t}
              onClick={() => pickTag(t)}
            >
              <span className="chip-label" title={t}>{t}</span> <span className="count">{n}</span>
            </button>
          ))}
        </div>
        {filtering && (
          <div className="filterbar" role="status">
            当前筛选：
            {q && <b>关键词「{q}」</b>}
            {tag && <b>标签「{tag}」</b>}
            <button
              type="button"
              className="link"
              onClick={() => {
                setQInput("");
                setTag("");
              }}
            >
              清除筛选
            </button>
          </div>
        )}
        <p className="error" role="alert">
          {err}
        </p>
        {list === null && !err && <p className="muted">加载中…</p>}
        {list && list.length === 0 && (
          <p className="empty">
            {filtering ? "没有符合条件的 Prompt。" : "还没有 Prompt。点击上方「新增 Prompt」创建第一条。"}
          </p>
        )}
        <ul className="cards">
          {list?.map((p) => (
            <li key={p.id}>
              <div
                role="button"
                tabIndex={0}
                className={`card${p.id === selectedId ? " selected" : ""}`}
                aria-current={p.id === selectedId}
                onClick={() => select(p.id)}
                onKeyDown={(e) => {
                  if (e.target === e.currentTarget && (e.key === "Enter" || e.key === " ")) {
                    e.preventDefault();
                    select(p.id);
                  }
                }}
              >
                <strong>{p.title}</strong>
                <span className="snippet">{p.body.slice(0, 80)}</span>
                <span className="chips">
                  {p.tags.map((t) => (
                    <TagChip key={t} tag={t} active={tag === t} onPick={pickTag} />
                  ))}
                </span>
                <span className="muted small">{formatTime(p.updated_at)}</span>
              </div>
            </li>
          ))}
        </ul>
      </section>

      <section className="detail-col" aria-label="Prompt 详情">
        {mode.kind === "new" && (
          <PromptForm
            key="new"
            heading="新增 Prompt"
            initial={{ title: "", body: "", tags: [] }}
            onSubmit={create}
            onCancel={cancel}
            onDirty={setDirty}
          />
        )}
        {mode.kind === "edit" && selected && (
          <PromptForm
            key={`edit-${selected.id}`}
            heading="编辑 Prompt"
            initial={{ title: selected.title, body: selected.body, tags: selected.tags }}
            onSubmit={update}
            onCancel={cancel}
            onDirty={setDirty}
          />
        )}
        {mode.kind === "view" && selected && (
          <article className="panel">
            <h2>{selected.title}</h2>
            <div className="chips">
              {selected.tags.length === 0 && <span className="muted small">无标签</span>}
              {selected.tags.map((t) => (
                <TagChip key={t} tag={t} active={tag === t} onPick={pickTag} />
              ))}
            </div>
            <pre className="body" data-testid="prompt-body">{selected.body}</pre>
            <p className="muted small">更新于 {formatTime(selected.updated_at)}</p>
            <div className="row wrap">
              <button type="button" onClick={copy}>
                复制正文
              </button>
              <button type="button" onClick={startEdit}>
                编辑
              </button>
              <button
                type="button"
                disabled={!modelConfigured}
                title={modelConfigured ? "" : "服务端未配置模型"}
                onClick={() => onLoadToWorkbench(selected.body)}
              >
                载入优化
              </button>
              <button type="button" className="danger" onClick={remove}>
                删除
              </button>
            </div>
            {!modelConfigured && <p className="muted small">服务端未配置模型，暂不能优化；库的其他功能不受影响。</p>}
            <p className="muted small" role="status">
              {notice}
            </p>
            {selected.source_run_id && <SourceRun key={selected.source_run_id} id={selected.source_run_id} />}
          </article>
        )}
        {mode.kind === "view" && !selected && (
          <div className="panel empty">
            <p>{selectedId !== null ? "该条目已不存在。" : "从左侧选择一条 Prompt 查看，或新增一条。"}</p>
            <p className="muted small" role="status">
              {notice}
            </p>
          </div>
        )}
      </section>
    </div>
  );
}

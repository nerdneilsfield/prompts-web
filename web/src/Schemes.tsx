import { useRef, useState } from "react";
import { api, describeError, type Scheme } from "./api";

interface Props {
  schemes: Scheme[];
  onChanged: (deletedOrEditedId?: number) => void;
}

// Scheme CRUD lives inside the workbench: a scheme only matters when optimizing.
export function Schemes({ schemes, onChanged }: Props) {
  const [editing, setEditing] = useState<Scheme | "new" | null>(null);
  const [name, setName] = useState("");
  const [body, setBody] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const lock = useRef(false);

  const open = (s: Scheme | "new") => {
    setEditing(s);
    setName(s === "new" ? "" : s.name);
    setBody(s === "new" ? "" : s.body);
    setErr("");
  };
  const save = async (e: React.FormEvent) => {
    e.preventDefault();
    if (lock.current || !editing) return;
    if (!name.trim() || !body.trim()) {
      setErr("方案名称和正文都不能为空。");
      return;
    }
    lock.current = true;
    setBusy(true);
    try {
      const payload = { name: name.trim(), body };
      if (editing === "new") await api("POST", "/api/schemes", payload);
      else await api("PUT", `/api/schemes/${editing.id}`, payload);
      const id = editing === "new" ? undefined : editing.id;
      setEditing(null);
      onChanged(id);
    } catch (e2) {
      setErr(describeError(e2));
    } finally {
      lock.current = false;
      setBusy(false);
    }
  };
  const remove = async (s: Scheme) => {
    if (!window.confirm(`确定删除方案「${s.name}」吗？已有的运行记录不受影响。`)) return;
    try {
      await api("DELETE", `/api/schemes/${s.id}`);
      if (editing !== "new" && editing?.id === s.id) setEditing(null);
      onChanged(s.id);
    } catch (e) {
      setErr(describeError(e));
    }
  };

  return (
    <div className="panel">
      <div className="row between">
        <h3>方案管理</h3>
        <button type="button" onClick={() => open("new")}>
          ＋ 新建方案
        </button>
      </div>
      {schemes.length === 0 && <p className="empty">还没有方案。新建一个方案后才能开始优化。</p>}
      <ul className="plain">
        {schemes.map((s) => (
          <li key={s.id} className="scheme-row">
            <span>
              <strong>{s.name}</strong>
              <span className="muted small snippet">{s.body.slice(0, 60)}</span>
            </span>
            <span className="row">
              <button type="button" onClick={() => open(s)} aria-label={`编辑方案 ${s.name}`}>
                编辑
              </button>
              <button type="button" className="danger" onClick={() => remove(s)} aria-label={`删除方案 ${s.name}`}>
                删除
              </button>
            </span>
          </li>
        ))}
      </ul>
      {editing && (
        <form onSubmit={save} className="form" aria-label="方案表单">
          <div className="field">
            <label htmlFor="sc-name">方案名称</label>
            <input id="sc-name" value={name} onChange={(e) => setName(e.target.value)} />
          </div>
          <div className="field">
            <label htmlFor="sc-body">方案正文（告诉模型怎么优化）</label>
            <textarea id="sc-body" rows={5} value={body} onChange={(e) => setBody(e.target.value)} />
          </div>
          <div className="row">
            <button type="submit" className="primary" disabled={busy} aria-busy={busy}>
              保存方案
            </button>
            <button type="button" onClick={() => setEditing(null)} disabled={busy}>
              取消
            </button>
          </div>
        </form>
      )}
      <p className="error" role="alert">
        {err}
      </p>
    </div>
  );
}

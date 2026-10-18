import { useId } from "react";

export function addTag(tags: string[], raw: string): string[] {
  const t = raw.trim();
  return t && !tags.includes(t) ? [...tags, t] : tags;
}

interface Props {
  tags: string[];
  draft: string;
  onTags: (t: string[]) => void;
  onDraft: (d: string) => void;
}

// Chip editor: Enter or comma commits the draft; the remove buttons keep the other tags intact.
export function TagInput({ tags, draft, onTags, onDraft }: Props) {
  const id = useId();
  const commit = () => {
    onTags(addTag(tags, draft));
    onDraft("");
  };
  return (
    <div className="field">
      <label htmlFor={id}>标签（可添加多个）</label>
      <div className="chips">
        {tags.map((t) => (
          <span className="chip chip-edit" key={t}>
            {t}
            <button type="button" aria-label={`移除标签 ${t}`} onClick={() => onTags(tags.filter((x) => x !== t))}>
              ×
            </button>
          </span>
        ))}
        {tags.length === 0 && <span className="muted small">暂无标签</span>}
      </div>
      <div className="row">
        <input
          id={id}
          value={draft}
          placeholder="输入标签后按 Enter"
          onChange={(e) => onDraft(e.target.value)}
          onKeyDown={(e) => {
            if (e.nativeEvent.isComposing) return;
            if (e.key === "Enter" || e.key === ",") {
              e.preventDefault();
              commit();
            }
          }}
        />
        <button type="button" onClick={commit} disabled={!draft.trim()}>
          添加标签
        </button>
      </div>
    </div>
  );
}

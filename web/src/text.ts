import { diffChars } from "diff";

export const charCount = (s: string) => [...s].length;

export function placeholders(s: string): Set<string> {
  return new Set([...s.matchAll(/\{\{\s*([^{}]+?)\s*\}\}/g)].map((m) => `{{${m[1]}}}`));
}

export function placeholderDelta(before: string, after: string) {
  const a = placeholders(before);
  const b = placeholders(after);
  return {
    lost: [...a].filter((x) => !b.has(x)),
    added: [...b].filter((x) => !a.has(x)),
  };
}

export interface Piece {
  text: string;
  kind: "same" | "add" | "del";
}

// Returns null when the texts are too large to diff in reasonable time.
export function charDiff(before: string, after: string): Piece[] | null {
  const parts = diffChars(before, after, { timeout: 1500 });
  if (!parts) return null;
  return parts.map((p) => ({ text: p.value, kind: p.added ? "add" : p.removed ? "del" : "same" }));
}

export async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.style.position = "fixed";
    ta.style.opacity = "0";
    document.body.appendChild(ta);
    ta.select();
    const ok = document.execCommand("copy");
    ta.remove();
    return ok;
  }
}

export function formatTime(iso: string): string {
  return new Date(iso).toLocaleString("zh-CN", { hour12: false });
}

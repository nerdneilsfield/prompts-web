export interface Prompt {
  id: number;
  title: string;
  body: string;
  tags: string[];
  source_run_id: string | null;
  created_at: string;
  updated_at: string;
}
export interface Scheme {
  id: number;
  name: string;
  body: string;
  created_at: string;
  updated_at: string;
}
export interface Run {
  request_id: string;
  input: string;
  scheme_id: number;
  scheme_name: string;
  scheme_snapshot: string;
  model: string;
  result: string;
  status: "running" | "succeeded" | "failed" | "interrupted";
  error_kind?: string;
  created_at: string;
  finished_at?: string;
}
export interface PromptInput {
  title: string;
  body: string;
  tags: string[];
  source_run_id?: string;
}

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
  ) {
    super(code);
  }
}

let onUnauthorized: () => void = () => {};
export function setUnauthorizedHandler(fn: () => void) {
  onUnauthorized = fn;
}

export async function apiRaw<T>(
  method: string,
  path: string,
  body?: unknown,
  signal?: AbortSignal,
): Promise<{ status: number; data: T }> {
  const res = await fetch(path, {
    method,
    signal,
    headers: body === undefined ? undefined : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (res.status === 204) return { status: 204, data: undefined as T };
  let data: unknown = null;
  try {
    data = await res.json();
  } catch {
    // non-JSON body (proxy error page); handled by the status check below
  }
  if (!res.ok) {
    if (res.status === 401 && path !== "/api/session") onUnauthorized();
    const code = (data as { error?: { code?: string } } | null)?.error?.code ?? `http_${res.status}`;
    throw new ApiError(res.status, code);
  }
  return { status: res.status, data: data as T };
}

export async function api<T>(method: string, path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  return (await apiRaw<T>(method, path, body, signal)).data;
}

const codeText: Record<string, string> = {
  unauthorized: "登录已失效，请重新登录。",
  bad_request: "内容不符合要求：标题和正文不能为空，标签最多 32 个、每个不超过 64 字。",
  bad_origin: "请求来源不被允许，请确认访问地址与服务端 APP_ORIGIN 一致。",
  bad_scheme: "所选方案已不存在，请重新选择方案。",
  not_found: "条目不存在，可能已被删除。",
  model_not_configured: "服务端未配置模型，无法优化。",
  rate_limited: "尝试次数过多，请稍后再试。",
  too_large: "内容太大，超出服务端限制。",
  request_id_conflict: "请求编号冲突，请重试。",
  internal: "服务端出错，请稍后重试。",
};

export function describeError(e: unknown): string {
  if (e instanceof ApiError) return codeText[e.code] ?? `请求失败（${e.status}）。`;
  return "无法连接服务器，请检查网络后重试。";
}

export const runErrorText: Record<string, string> = {
  auth: "服务端的模型密钥无效，重试无用，请联系管理员。",
  rate_limit: "模型服务限流，请稍后重试。",
  upstream: "模型服务出错，请重试。",
  invalid: "模型返回了无法使用的内容，请重试。",
  timeout: "模型响应超时，请重试。",
  canceled: "本次请求已被取消。",
  interrupted: "服务重启中断了本次运行，请重新执行。",
};

// crypto.randomUUID needs a secure context; plain-HTTP LAN access does not have one.
export function newRequestId(): string {
  const b = crypto.getRandomValues(new Uint8Array(16));
  return Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
}

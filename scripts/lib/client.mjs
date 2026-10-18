// Minimal client for this project's own HTTP API. Nothing else is needed:
// every endpoint is JSON over fetch, so there is no SDK to depend on.
//
// Write requests carry no Origin header, which the server treats as a non-browser
// client and allows. Login is the exception-free path documented in docs/api.md.

export class ApiClient {
  #baseUrl;
  #cookie;

  constructor(baseUrl, cookie) {
    this.#baseUrl = new URL(baseUrl);
    this.#cookie = cookie;
  }

  async #request(method, path, body) {
    const res = await fetch(new URL(path, this.#baseUrl), {
      method,
      headers: {
        ...(body === undefined ? {} : { "Content-Type": "application/json" }),
        ...(this.#cookie ? { Cookie: this.#cookie } : {}),
      },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    const text = await res.text();
    let data = null;
    try {
      data = text === "" ? null : JSON.parse(text);
    } catch {
      data = text;
    }
    if (!res.ok) {
      const detail = data && typeof data === "object" && data.error ? data.error.code : String(data).slice(0, 200);
      throw new Error(`${method} ${path} failed: HTTP ${res.status} ${detail}`);
    }
    return data;
  }

  get(path) {
    return this.#request("GET", path);
  }

  post(path, body) {
    return this.#request("POST", path, body ?? {});
  }

  delete(path) {
    return this.#request("DELETE", path);
  }
}

// Logs in and returns a client plus the model_configured flag the login
// response already carries, so callers need no second request.
export async function login(baseUrl, password) {
  const url = new URL("/api/session", baseUrl);
  const res = await fetch(url, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ password }),
  });
  const text = await res.text();
  let data = null;
  try {
    data = text === "" ? null : JSON.parse(text);
  } catch {
    data = null;
  }
  if (res.status !== 200) {
    throw new Error(`login failed: HTTP ${res.status} ${data?.error?.code ?? text.slice(0, 200)}`);
  }
  const setCookie = res.headers.getSetCookie?.()[0] ?? "";
  const cookie = setCookie.split(";")[0];
  if (!cookie) throw new Error("login returned 200 but no session cookie");
  return { client: new ApiClient(baseUrl, cookie), modelConfigured: data?.model_configured === true };
}

// Shared argument parsing: --flag value and --flag=value, nothing else.
export function parseArgs(argv) {
  const args = {};
  for (let i = 0; i < argv.length; i += 1) {
    const token = argv[i];
    if (!token.startsWith("--")) throw new Error(`unexpected argument: ${token}`);
    const eq = token.indexOf("=");
    if (eq !== -1) {
      args[token.slice(2, eq)] = token.slice(eq + 1);
    } else {
      const next = argv[i + 1];
      if (next === undefined || next.startsWith("--")) {
        args[token.slice(2)] = true;
      } else {
        args[token.slice(2)] = next;
        i += 1;
      }
    }
  }
  return args;
}

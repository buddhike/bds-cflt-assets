// Typed access to the Launchpad HTTP API. The session lives in an HttpOnly
// cookie, so nothing here can read or store it.

export interface Credentials {
  configured: boolean;
  apiKeyHint?: string;
  updatedAt?: number;
}

export interface User {
  email: string;
  role: 'admin';
  createdAt: number;
  credentials?: Credentials;
}

export type DemoStatus =
  | 'launching'
  | 'running'
  | 'launch_failed'
  | 'updating'
  | 'update_failed'
  | 'tearing_down'
  | 'teardown_failed';

export interface DemoState {
  status: DemoStatus;
  url: string;
  error: string;
  logTail?: string;
  launchedBy: string;
  updatedAt: number;
  /** The commit that was applied, or is being applied. */
  ref: string;
  canTearDown: boolean;
  canUpdate: boolean;
  /** A running demo is at an older commit than the one now in the repository. */
  updateAvailable: boolean;
}

export interface Demo {
  id: string;
  name: string;
  description: string;
  /** The Confluent Cloud products the demo shows. Absent from an older server. */
  tags?: string[];
  /** The commit the demo was read at. */
  ref: string;
  /** Whether `ref` was the tip of the main branch when the repository was last read. */
  latest: boolean;
  valid: boolean;
  problems: string[];
  needsConfluent: boolean;
  state: DemoState | null;
}

export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
  ) {
    super(message);
  }
}

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(`/api${path}`, {
    method,
    credentials: 'same-origin',
    headers: {'X-Launchpad': '1', ...(body === undefined ? {} : {'Content-Type': 'application/json'})},
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  let data: unknown = null;
  try {
    data = await res.json();
  } catch {
    // An empty or non-JSON body is reported by status below.
  }
  if (!res.ok) {
    const e = (data as {error?: {code?: string; message?: string}} | null)?.error;
    throw new ApiError(res.status, e?.code ?? 'error', e?.message ?? `The request failed (${res.status})`);
  }
  return data as T;
}

export const api = {
  me: () => call<User>('GET', '/me'),
  /** Always answers that a code was sent, whatever the number is. Only a malformed number is refused. */
  start: (phone: string) => call<{sent: boolean}>('POST', '/auth/start', {phone}),
  verify: (otp: string) => call<{user: User}>('POST', '/auth/verify', {otp}),
  logout: () => call<{ok: boolean}>('POST', '/auth/logout'),
  demos: () => call<{demos: Demo[]}>('GET', '/demos'),
  demo: (id: string) => call<Demo>('GET', `/demos/${encodeURIComponent(id)}`),
  refreshDemos: () => call<{demos: Demo[]}>('POST', '/demos/refresh'),
  launch: (id: string) => call<Demo>('POST', `/demos/${encodeURIComponent(id)}/launch`),
  update: (id: string) => call<Demo>('POST', `/demos/${encodeURIComponent(id)}/update`),
  teardown: (id: string) => call<Demo>('POST', `/demos/${encodeURIComponent(id)}/teardown`),
  credentials: () => call<Credentials>('GET', '/credentials'),
  saveCredentials: (apiKey: string, apiSecret: string) =>
    call<Credentials>('PUT', '/credentials', {apiKey, apiSecret}),
  deleteCredentials: () => call<Credentials>('DELETE', '/credentials'),
};

export function errorText(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

/** A demo's address is whatever its Terraform printed, so only open web addresses. */
export function safeUrl(url: string): string | null {
  return /^https?:\/\/[^\s]+$/i.test(url) ? url : null;
}

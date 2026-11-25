// Typed access to the Launchpad HTTP API. The session lives in an HttpOnly
// cookie, so nothing here can read or store it.

export interface Credentials {
  configured: boolean;
  apiKeyHint?: string;
  updatedAt?: number;
}

export interface User {
  email: string;
  status: 'pending' | 'approved' | 'denied';
  role: 'admin' | 'user';
  createdAt: number;
  decidedBy?: string | null;
  decidedAt?: number | null;
  credentials?: Credentials;
}

export type DemoStatus = 'launching' | 'running' | 'launch_failed' | 'tearing_down' | 'teardown_failed';

export interface DemoState {
  status: DemoStatus;
  url: string;
  error: string;
  logTail?: string;
  launchedBy: string;
  updatedAt: number;
  ref: string;
  canTearDown: boolean;
}

export interface Demo {
  id: string;
  name: string;
  description: string;
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
  start: (email: string) => call<{sent: boolean}>('POST', '/auth/start', {email}),
  verify: (email: string, otp: string) =>
    call<{status: User['status']; user: User}>('POST', '/auth/verify', {email, otp}),
  logout: () => call<{ok: boolean}>('POST', '/auth/logout'),
  demos: () => call<{demos: Demo[]}>('GET', '/demos'),
  demo: (id: string) => call<Demo>('GET', `/demos/${encodeURIComponent(id)}`),
  refreshDemos: () => call<{demos: Demo[]}>('POST', '/demos/refresh'),
  launch: (id: string) => call<Demo>('POST', `/demos/${encodeURIComponent(id)}/launch`),
  teardown: (id: string) => call<Demo>('POST', `/demos/${encodeURIComponent(id)}/teardown`),
  credentials: () => call<Credentials>('GET', '/credentials'),
  saveCredentials: (apiKey: string, apiSecret: string) =>
    call<Credentials>('PUT', '/credentials', {apiKey, apiSecret}),
  deleteCredentials: () => call<Credentials>('DELETE', '/credentials'),
  requests: () => call<{requests: User[]}>('GET', '/admin/requests'),
  users: () => call<{users: User[]}>('GET', '/admin/users'),
  decide: (email: string, approve: boolean) =>
    call<User>('POST', `/admin/users/${encodeURIComponent(email)}/${approve ? 'approve' : 'deny'}`),
};

export function errorText(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

/** A demo's address is whatever its Terraform printed, so only open web addresses. */
export function safeUrl(url: string): string | null {
  return /^https?:\/\/[^\s]+$/i.test(url) ? url : null;
}

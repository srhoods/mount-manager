export interface Host { id: number; hostname: string; agent_version: string; last_seen: string | null; status: 'in-sync' | 'pending' | 'unknown'; problems: string }
export interface Template { id: number; name: string; fstype: string; source: string; mountpoint: string; options: string; version: number }
export interface Group { id: number; name: string; priority: number; host_regex: string; templates: string[]; members: string[] }
export interface AuditEvent { id: number; ts: string; actor: string; host: string; action: string; detail: string }
export interface HostDetail {
  hostname: string
  desired: { source: string; mountpoint: string; fstype: string; options: string; version: number }[]
  state: { mountpoint: string; state: string; error: string; updated_at: string }[]
}

const KEY = 'mm.session'
export interface Session { token: string; role: string; user: string }
// "Keep me signed in" stores the session in localStorage (shared across tabs); otherwise per-tab sessionStorage.
const read = (st: Storage) => { try { return JSON.parse(st.getItem(KEY) || 'null') as Session | null } catch { return null } }
export const getSession = (): Session | null => read(localStorage) ?? read(sessionStorage)
export const setSession = (s: Session | null, persist = true) => {
  try {
    localStorage.removeItem(KEY); sessionStorage.removeItem(KEY)
    if (s) (persist ? localStorage : sessionStorage).setItem(KEY, JSON.stringify(s))
  } catch { /* storage unavailable */ }
}

export class ApiError extends Error { constructor(public status: number, msg: string) { super(msg) } }
let onUnauthorized: () => void = () => {}
export const setUnauthorizedHandler = (f: () => void) => { onUnauthorized = f }

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  const s = getSession()
  const r = await fetch(path, {
    method,
    headers: { ...(body !== undefined ? { 'Content-Type': 'application/json' } : {}), ...(s ? { Authorization: `Bearer ${s.token}` } : {}) },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  })
  const text = await r.text()
  const data = text ? JSON.parse(text) : null
  if (!r.ok) {
    if (r.status === 401 && path !== '/api/login') onUnauthorized()
    throw new ApiError(r.status, data?.error || `HTTP ${r.status}`)
  }
  return data as T
}

export const api = {
  login: (username: string, password: string) => call<{ token: string; role: string }>('POST', '/api/login', { username, password }),
  hosts: () => call<Host[]>('GET', '/api/hosts'),
  host: (id: number) => call<HostDetail>('GET', `/api/hosts/${id}/mounts`),
  deleteHost: (id: number) => call('DELETE', `/api/hosts/${id}`),
  templates: () => call<Template[]>('GET', '/api/templates'),
  saveTemplate: (t: { name: string; fstype: string; source: string; mountpoint: string; options: string }) => call<{ id: number }>('POST', '/api/templates', t),
  deleteTemplate: (id: number) => call('DELETE', `/api/templates/${id}`),
  groups: () => call<Group[]>('GET', '/api/groups'),
  saveGroup: (g: { name: string; priority: number; host_regex: string }) => call<{ id: number }>('POST', '/api/groups', g),
  deleteGroup: (id: number) => call('DELETE', `/api/groups/${id}`),
  link: (add: boolean, gid: number, kind: 'templates' | 'members', id: number) => call(add ? 'POST' : 'DELETE', `/api/groups/${gid}/${kind}/${id}`),
  createToken: (note: string, hours: number, uses: number) => call<{ token: string }>('POST', '/api/tokens', { note, hours, uses }),
  audit: (host = '', limit = 200) => call<AuditEvent[]>('GET', `/api/audit?host=${encodeURIComponent(host)}&limit=${limit}`),
}

export type HostState = 'in-sync' | 'pending' | 'offline' | 'unknown'
export interface Host { id: number; hostname: string; agent_version: string; last_seen: string | null; state: HostState; problems: string }
export interface HostBrief { id: number; hostname: string }
export interface HostSummary { total: number; in_sync: number; pending: number; offline: number; unknown: number }
export interface HostQuery { q?: string; state?: string; limit?: number; offset?: number }
export interface MountQuery { name?: string; source?: string; mountpoint?: string; type?: string; limit?: number; offset?: number }
export interface ServerVersion { version: string; commit: string; built: string; started: string; uptime_seconds: number }
export type TokenStatus = 'active' | 'expired' | 'used' | 'revoked'
export interface EnrolToken {
  id: number; note: string; created_at: string; created_by: string; expires: string
  uses_left: number; uses_total: number; status: TokenStatus; seconds_left: number
}
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

async function callFull<T>(method: string, path: string, body?: unknown): Promise<{ data: T; total: number }> {
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
  return { data: data as T, total: parseInt(r.headers.get('X-Total-Count') || '-1', 10) }
}
const call = <T>(method: string, path: string, body?: unknown) => callFull<T>(method, path, body).then(r => r.data)

export const api = {
  login: (username: string, password: string) => call<{ token: string; role: string }>('POST', '/api/login', { username, password }),
  // server-side filtering and pagination; total is the number of hosts matching the filter
  hosts: (p: HostQuery = {}) => {
    const qs = new URLSearchParams()
    if (p.q) qs.set('q', p.q)
    if (p.state && p.state !== 'all') qs.set('state', p.state)
    if (p.limit) qs.set('limit', String(p.limit))
    if (p.offset) qs.set('offset', String(p.offset))
    return callFull<Host[]>('GET', `/api/hosts?${qs}`).then(r => ({ items: r.data, total: r.total }))
  },
  hostNames: () => call<HostBrief[]>('GET', '/api/hosts?brief=1'),
  version: () => call<ServerVersion>('GET', '/api/version'),
  hostSummary: () => call<HostSummary>('GET', '/api/hosts/summary'),
  host: (id: number) => call<HostDetail>('GET', `/api/hosts/${id}/mounts`),
  deleteHost: (id: number) => call('DELETE', `/api/hosts/${id}`),
  templates: () => call<Template[]>('GET', '/api/templates'),   // everything (group editor, CLI-style callers)
  // server-side search and pagination for the Mounts page; total is the number of mounts matching the filters
  mounts: (p: MountQuery = {}) => {
    const qs = new URLSearchParams()
    for (const k of ['name', 'source', 'mountpoint', 'type'] as const) if (p[k]) qs.set(k, p[k]!)
    if (p.limit) qs.set('limit', String(p.limit))
    if (p.offset) qs.set('offset', String(p.offset))
    return callFull<Template[]>('GET', `/api/templates?${qs}`).then(r => ({ items: r.data, total: r.total }))
  },
  saveTemplate: (t: { name: string; fstype: string; source: string; mountpoint: string; options: string }) => call<{ id: number }>('POST', '/api/templates', t),
  deleteTemplate: (id: number) => call('DELETE', `/api/templates/${id}`),
  // copies a mount under a new name; never overwrites (409 if the name is taken); group assignments are not copied
  cloneTemplate: (id: number, name: string) => call<{ id: number }>('POST', `/api/templates/${id}/clone`, { name }),
  groups: () => call<Group[]>('GET', '/api/groups'),
  saveGroup: (g: { name: string; priority: number; host_regex: string }) => call<{ id: number }>('POST', '/api/groups', g),
  deleteGroup: (id: number) => call('DELETE', `/api/groups/${id}`),
  link: (add: boolean, gid: number, kind: 'templates' | 'members', id: number) => call(add ? 'POST' : 'DELETE', `/api/groups/${gid}/${kind}/${id}`),
  createToken: (note: string, hours: number, uses: number) => call<{ token: string; id: number }>('POST', '/api/tokens', { note, hours, uses }),
  tokens: (all = false) => call<EnrolToken[]>('GET', `/api/tokens${all ? '?all=1' : ''}`),
  revokeToken: (id: number) => call('DELETE', `/api/tokens/${id}`),
  audit: (host = '', limit = 200) => call<AuditEvent[]>('GET', `/api/audit?host=${encodeURIComponent(host)}&limit=${limit}`),
}

import { FormEvent, useEffect, useState } from 'react'
import { api, getSession, setSession, setUnauthorizedHandler, Session } from './api'
import { ErrorBox, Modal, duration, fmtTime, useLoad } from './ui'
import Dashboard from './pages/Dashboard'
import Hosts from './pages/Hosts'
import Mounts from './pages/Mounts'
import Groups from './pages/Groups'
import Enrol from './pages/Enrol'
import Audit from './pages/Audit'

const NAV = [
  ['dashboard', 'Dashboard'], ['hosts', 'Hosts'], ['groups', 'Groups'], ['mounts', 'Mounts'], ['enrol', 'Enrolment'], ['audit', 'Audit log'],
] as const

// '#/templates' was renamed '#/mounts'; keep old bookmarks working
const route = () => { const r = location.hash.replace(/^#\/?/, '').split('/')[0] || 'dashboard'; return r === 'templates' ? 'mounts' : r }

function Login({ onLogin }: { onLogin: (s: Session, keep: boolean) => void }) {
  const [keep, setKeep] = useState(true), [u, setU] = useState(''), [p, setP] = useState(''), [err, setErr] = useState<string | null>(null), [busy, setBusy] = useState(false)
  const submit = async (e: FormEvent) => {
    e.preventDefault(); setBusy(true); setErr(null)
    try { const r = await api.login(u, p); onLogin({ token: r.token, role: r.role, user: u }, keep) }
    catch (x) { setErr((x as Error).message) } finally { setBusy(false) }
  }
  return (
    <div className="login-wrap">
      <form className="card login" onSubmit={submit}>
        <div className="brand big"><span className="logo" />Mount Manager</div>
        <label>Username<input autoFocus autoComplete="username" value={u} onChange={e => setU(e.target.value)} /></label>
        <label>Password<input type="password" autoComplete="current-password" value={p} onChange={e => setP(e.target.value)} /></label>
        <label className="check"><input type="checkbox" checked={keep} onChange={e => setKeep(e.target.checked)} />Keep me signed in on this browser</label>
        <ErrorBox err={err} />
        <button className="primary" disabled={busy || !u || !p}>{busy ? 'Signing in…' : 'Sign in'}</button>
      </form>
    </div>
  )
}

// The bundle is served by the server it was built with, so a different version here means this tab predates an upgrade.
const UI_VERSION = __APP_VERSION__

function About({ session, onClose, ver }: { session: Session; onClose: () => void; ver: { version: string; commit: string; built: string; started: string; uptime_seconds: number } | null }) {
  return (
    <Modal title="About Mount Manager" onClose={onClose}>
      <dl className="facts compact about">
        <div><dt>Server version</dt><dd><b>{ver ? ver.version : '…'}</b></dd></div>
        <div><dt>Commit</dt><dd className="mono">{ver?.commit ?? '…'}</dd></div>
        <div><dt>Built</dt><dd>{ver ? (ver.built === 'unknown' ? 'unknown' : fmtTime(ver.built)) : '…'}</dd></div>
        <div><dt>Server running since</dt><dd>{ver ? `${fmtTime(ver.started)} (up ${duration(ver.uptime_seconds)})` : '…'}</dd></div>
        <div><dt>This page (UI)</dt><dd>{UI_VERSION}{ver && ver.version !== 'dev' && ver.version !== UI_VERSION && <span className="warn-text"> — differs from the server, reload</span>}</dd></div>
        <div><dt>Signed in as</dt><dd>{session.user} ({session.role})</dd></div>
      </dl>
      <footer><button className="primary" onClick={onClose}>Close</button></footer>
    </Modal>
  )
}

function Shell({ session, page, logout }: { session: Session; page: string; logout: () => void }) {
  const canWrite = session.role === 'admin' || session.role === 'operator'
  const isAdmin = session.role === 'admin'
  const ver = useLoad(api.version, 5 * 60 * 1000)   // also notices an upgrade while the page stays open
  const [about, setAbout] = useState(false)
  const v = ver.data
  const stale = !!v && v.version !== 'dev' && UI_VERSION !== 'dev' && v.version !== UI_VERSION
  return (
    <div className="shell">
      <aside>
        <div className="brand"><span className="logo" />Mount Manager</div>
        <nav>{NAV.map(([k, l]) => <a key={k} href={`#/${k}`} className={page === k ? 'active' : ''}>{l}</a>)}</nav>
        <button className="ver" onClick={() => setAbout(true)} title={v ? `Commit ${v.commit}, built ${v.built}. Click for details.` : 'Server version'}>
          {v ? `Server v${v.version}` : ver.error ? 'Version unavailable' : 'Server version…'}
        </button>
        <div className="who"><div><b>{session.user}</b><small>{session.role}</small></div><button onClick={logout}>Sign out</button></div>
      </aside>
      <main>
        {stale && <div className="banner" role="status">Mount Manager was updated: this page is v{UI_VERSION} but the server is now v{v!.version}. <button onClick={() => location.reload()}>Reload</button></div>}
        {page === 'dashboard' && <Dashboard />}
        {page === 'hosts' && <Hosts canWrite={canWrite} />}
        {page === 'groups' && <Groups canWrite={canWrite} />}
        {page === 'mounts' && <Mounts canWrite={canWrite} />}
        {page === 'enrol' && <Enrol canWrite={isAdmin} />}
        {page === 'audit' && <Audit />}
      </main>
      {about && <About session={session} ver={v ?? null} onClose={() => setAbout(false)} />}
    </div>
  )
}

export default function App() {
  const [session, setSess] = useState<Session | null>(getSession())
  const [page, setPage] = useState(route())
  const logout = () => { setSession(null); setSess(null) }
  useEffect(() => { setUnauthorizedHandler(logout) }, [])
  useEffect(() => { const h = () => setPage(route()); window.addEventListener('hashchange', h); return () => window.removeEventListener('hashchange', h) }, [])

  if (!session) return <Login onLogin={(s, keep) => { setSession(s, keep); setSess(s) }} />
  return <Shell session={session} page={page} logout={logout} />
}

import { FormEvent, useEffect, useState } from 'react'
import { api, getSession, setSession, setUnauthorizedHandler, Session } from './api'
import { ErrorBox } from './ui'
import Dashboard from './pages/Dashboard'
import Hosts from './pages/Hosts'
import Templates from './pages/Templates'
import Groups from './pages/Groups'
import Enrol from './pages/Enrol'
import Audit from './pages/Audit'

const NAV = [
  ['dashboard', 'Dashboard'], ['hosts', 'Hosts'], ['groups', 'Groups'], ['templates', 'Templates'], ['enrol', 'Enrolment'], ['audit', 'Audit log'],
] as const

const route = () => (location.hash.replace(/^#\/?/, '').split('/')[0] || 'dashboard')

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

export default function App() {
  const [session, setSess] = useState<Session | null>(getSession())
  const [page, setPage] = useState(route())
  const logout = () => { setSession(null); setSess(null) }
  useEffect(() => { setUnauthorizedHandler(logout) }, [])
  useEffect(() => { const h = () => setPage(route()); window.addEventListener('hashchange', h); return () => window.removeEventListener('hashchange', h) }, [])

  if (!session) return <Login onLogin={(s, keep) => { setSession(s, keep); setSess(s) }} />
  const canWrite = session.role === 'admin' || session.role === 'operator'
  const isAdmin = session.role === 'admin'
  return (
    <div className="shell">
      <aside>
        <div className="brand"><span className="logo" />Mount Manager</div>
        <nav>{NAV.map(([k, l]) => <a key={k} href={`#/${k}`} className={page === k ? 'active' : ''}>{l}</a>)}</nav>
        <div className="who"><div><b>{session.user}</b><small>{session.role}</small></div><button onClick={logout}>Sign out</button></div>
      </aside>
      <main>
        {page === 'dashboard' && <Dashboard />}
        {page === 'hosts' && <Hosts canWrite={canWrite} />}
        {page === 'groups' && <Groups canWrite={canWrite} />}
        {page === 'templates' && <Templates canWrite={canWrite} />}
        {page === 'enrol' && <Enrol canWrite={isAdmin} />}
        {page === 'audit' && <Audit />}
      </main>
    </div>
  )
}

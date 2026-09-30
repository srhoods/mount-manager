import { Component, ErrorInfo, ReactNode, useCallback, useEffect, useRef, useState } from 'react'

export function timeAgo(ts: string | null): string {
  if (!ts) return 'never'
  const s = Math.max(0, (Date.now() - new Date(ts).getTime()) / 1000)
  if (s < 60) return 'just now'
  if (s < 3600) return `${Math.floor(s / 60)}m ago`
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`
  return `${Math.floor(s / 86400)}d ago`
}
export const fmtTime = (ts: string) => new Date(ts).toLocaleString()

// State labels are upper case everywhere they are shown.
const LABEL: Record<string, string> = {
  offline: 'OFFLINE', pending: 'PENDING', 'in-sync': 'IN SYNC', unknown: 'UNKNOWN', ok: 'OK', failed: 'FAILED',
  active: 'ACTIVE', expired: 'EXPIRED', used: 'USED UP', revoked: 'REVOKED',
}
export function Badge({ kind }: { kind: string }) {
  return <span className={`badge b-${kind}`}><i />{LABEL[kind] ?? kind.toUpperCase()}</span>
}

export function Modal({ title, onClose, children, wide, xl }: { title: string; onClose: () => void; children: ReactNode; wide?: boolean; xl?: boolean }) {
  useEffect(() => {
    const h = (e: KeyboardEvent) => e.key === 'Escape' && onClose()
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  }, [onClose])
  return (
    <div className="overlay" onMouseDown={e => e.target === e.currentTarget && onClose()}>
      <div className={`modal${wide ? ' wide' : ''}${xl ? ' xl' : ''}`} role="dialog" aria-modal="true" aria-label={title}>
        <header><h2>{title}</h2><button className="icon" aria-label="Close" onClick={onClose}>✕</button></header>
        {children}
      </div>
    </div>
  )
}

export function Empty({ children }: { children: ReactNode }) { return <div className="empty">{children}</div> }
export function ErrorBox({ err }: { err: string | null }) { return err ? <div className="error" role="alert">{err}</div> : null }

/** Load data on mount and (optionally) poll. */
export function useLoad<T>(fn: () => Promise<T>, pollMs = 0, deps: unknown[] = []) {
  const [data, setData] = useState<T | null>(null)
  const [error, setError] = useState<string | null>(null)
  const fnRef = useRef(fn); fnRef.current = fn
  const reload = useCallback(() => fnRef.current().then(d => { setData(d); setError(null) }).catch(e => setError(e.message)), [])
  useEffect(() => {
    const d = setTimeout(reload, deps.length ? 250 : 0) // debounce filter typing
    if (!pollMs) return () => clearTimeout(d)
    const t = setInterval(reload, pollMs)
    return () => { clearTimeout(d); clearInterval(t) }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [reload, pollMs, ...deps])
  return { data, error, reload }
}

export function useToast() {
  const [msg, setMsg] = useState<string | null>(null)
  const show = useCallback((m: string) => { setMsg(m); setTimeout(() => setMsg(null), 3000) }, [])
  return { toast: msg ? <div className="toast" role="status">{msg}</div> : null, show }
}

/** "3d 4h", "5h 12m", "9m", "<1m" */
export function duration(secs: number): string {
  if (secs <= 0) return '—'
  if (secs < 60) return '<1m'
  const d = Math.floor(secs / 86400), h = Math.floor((secs % 86400) / 3600), m = Math.floor((secs % 3600) / 60)
  if (d > 0) return `${d}d ${h}h`
  if (h > 0) return `${h}h ${m}m`
  return `${m}m`
}

/** Turns a render error into a message with a way out, instead of a blank page. */
export class ErrorBoundary extends Component<{ children: ReactNode }, { error: Error | null }> {
  state = { error: null as Error | null }
  static getDerivedStateFromError(error: Error) { return { error } }
  componentDidCatch(error: Error, info: ErrorInfo) { console.error('UI error:', error, info.componentStack) }
  render() {
    if (!this.state.error) return this.props.children
    return (
      <div className="login-wrap">
        <div className="card login" role="alert">
          <h2>Something went wrong</h2>
          <p className="muted">The page hit an unexpected error. Your session is unaffected.</p>
          <pre className="small">{this.state.error.message}</pre>
          <button className="primary" onClick={() => { this.setState({ error: null }); location.reload() }}>Reload</button>
        </div>
      </div>
    )
  }
}

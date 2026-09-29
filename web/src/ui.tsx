import { ReactNode, useCallback, useEffect, useRef, useState } from 'react'
import type { Host } from './api'

export function timeAgo(ts: string | null): string {
  if (!ts) return 'never'
  const s = Math.max(0, (Date.now() - new Date(ts).getTime()) / 1000)
  if (s < 60) return 'just now'
  if (s < 3600) return `${Math.floor(s / 60)}m ago`
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`
  return `${Math.floor(s / 86400)}d ago`
}
export const fmtTime = (ts: string) => new Date(ts).toLocaleString()

// A host that has not polled for 3 intervals (default 5 min) is treated as offline.
export const isStale = (h: Host) => !h.last_seen || Date.now() - new Date(h.last_seen).getTime() > 15 * 60 * 1000
export function hostState(h: Host): 'offline' | 'pending' | 'in-sync' | 'unknown' {
  if (isStale(h)) return 'offline'
  return h.status
}

const LABEL: Record<string, string> = { offline: 'Offline', pending: 'Pending', 'in-sync': 'In sync', unknown: 'Unknown', ok: 'OK', failed: 'Failed' }
export function Badge({ kind }: { kind: string }) {
  return <span className={`badge b-${kind}`}><i />{LABEL[kind] ?? kind}</span>
}

export function Modal({ title, onClose, children, wide }: { title: string; onClose: () => void; children: ReactNode; wide?: boolean }) {
  useEffect(() => {
    const h = (e: KeyboardEvent) => e.key === 'Escape' && onClose()
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  }, [onClose])
  return (
    <div className="overlay" onMouseDown={e => e.target === e.currentTarget && onClose()}>
      <div className={`modal${wide ? ' wide' : ''}`} role="dialog" aria-modal="true" aria-label={title}>
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

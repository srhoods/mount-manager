import { useEffect, useState } from 'react'
import { api, Host } from '../api'
import { Badge, Empty, ErrorBox, fmtTime, Modal, timeAgo, useLoad, useToast } from '../ui'

const PAGE_SIZES = [25, 50, 100, 250]
const SIZE_KEY = 'mm.hosts.pageSize'
const loadSize = () => { try { const n = parseInt(localStorage.getItem(SIZE_KEY) || '', 10); return PAGE_SIZES.includes(n) ? n : 50 } catch { return 50 } }

function HostDetail({ host, canWrite, onClose, onDeleted }: { host: Host; canWrite: boolean; onClose: () => void; onDeleted: () => void }) {
  const d = useLoad(() => api.host(host.id), 15000, [host.id])
  const a = useLoad(() => api.audit(host.hostname, 25), 15000, [host.hostname])
  const [err, setErr] = useState<string | null>(null)
  const desired = d.data?.desired ?? []          // tolerate null lists from the API
  const state = new Map((d.data?.state ?? []).map(s => [s.mountpoint, s]))
  const remove = async () => {
    if (!confirm(`Remove ${host.hostname}? It will be able to re-enrol with a valid token.`)) return
    try { await api.deleteHost(host.id); onDeleted() } catch (e) { setErr((e as Error).message) }
  }
  return (
    <Modal title={host.hostname} onClose={onClose} xl>
      <ErrorBox err={err || d.error} />
      <dl className="facts">
        <div><dt>State</dt><dd><Badge kind={host.state} /></dd></div>
        <div><dt>Last seen</dt><dd>{host.last_seen ? `${fmtTime(host.last_seen)} (${timeAgo(host.last_seen)})` : 'never'}</dd></div>
        <div><dt>Agent</dt><dd>{host.agent_version || '—'}</dd></div>
      </dl>
      <h3>Mounts</h3>
      {!d.data ? <Empty>Loading…</Empty> : desired.length === 0 ? <Empty>No mounts are assigned to this host. Check group membership.</Empty> : (
        <div className="scroll-x">
          <table className="fit"><thead><tr><th>Mountpoint</th><th>Source</th><th>Type</th><th>Options</th><th>Status</th></tr></thead><tbody>
            {desired.map(m => {
              const s = state.get(m.mountpoint)
              return <tr key={m.mountpoint}><td className="mono nowrap">{m.mountpoint}</td><td className="mono nowrap">{m.source}</td><td>{m.fstype}</td><td className="mono nowrap">{m.options}</td>
                <td className="nowrap">{s ? <><Badge kind={s.state} />{s.error && <span className="err-text small"> {s.error}</span>}</> : <Badge kind="unknown" />}</td></tr>
            })}
          </tbody></table>
        </div>
      )}
      <h3>Recent events</h3>
      {!a.data ? <Empty>Loading…</Empty> : a.data.length === 0 ? <Empty>No events.</Empty> : (
        <div className="scroll-x">
          <table className="fit events"><thead><tr><th>Time</th><th>Action</th><th>Detail</th></tr></thead><tbody>
            {a.data.map(e => <tr key={e.id}><td className="nowrap muted">{fmtTime(e.ts)}</td><td className="nowrap"><b>{e.action}</b></td><td className="wrap muted">{e.detail}</td></tr>)}
          </tbody></table>
        </div>
      )}
      {canWrite && <footer><button className="danger" onClick={remove}>Remove host</button></footer>}
    </Modal>
  )
}

function Pager({ total, page, size, onPage, onSize }: { total: number; page: number; size: number; onPage: (p: number) => void; onSize: (s: number) => void }) {
  const pages = Math.max(1, Math.ceil(total / size))
  const from = total === 0 ? 0 : page * size + 1, to = Math.min(total, (page + 1) * size)
  return (
    <div className="pager">
      <span className="muted">{total === 0 ? 'No hosts' : `Showing ${from.toLocaleString()}–${to.toLocaleString()} of ${total.toLocaleString()}`}</span>
      <span className="spacer" />
      <label className="inline">Rows per page
        <select value={size} onChange={e => onSize(parseInt(e.target.value, 10))} aria-label="Rows per page">
          {PAGE_SIZES.map(n => <option key={n} value={n}>{n}</option>)}
        </select>
      </label>
      <div className="pager-nav" role="navigation" aria-label="Pagination">
        <button onClick={() => onPage(0)} disabled={page === 0} aria-label="First page">«</button>
        <button onClick={() => onPage(page - 1)} disabled={page === 0} aria-label="Previous page">‹ Prev</button>
        <span className="pager-pos">Page {(page + 1).toLocaleString()} of {pages.toLocaleString()}</span>
        <button onClick={() => onPage(page + 1)} disabled={page >= pages - 1} aria-label="Next page">Next ›</button>
        <button onClick={() => onPage(pages - 1)} disabled={page >= pages - 1} aria-label="Last page">»</button>
      </div>
    </div>
  )
}

export default function Hosts({ canWrite }: { canWrite: boolean }) {
  const [q, setQ] = useState(''), [filter, setFilter] = useState('all'), [sel, setSel] = useState<Host | null>(null)
  const [size, setSizeState] = useState(loadSize), [page, setPage] = useState(0)
  const hosts = useLoad(() => api.hosts({ q, state: filter, limit: size, offset: page * size }), 15000, [q, filter, size, page])
  const { toast, show } = useToast()
  const total = hosts.data?.total ?? 0
  const rows = hosts.data?.items ?? []
  const lastPage = Math.max(0, Math.ceil(total / size) - 1)
  useEffect(() => { if (hosts.data && page > lastPage) setPage(lastPage) }, [hosts.data, page, lastPage])   // e.g. after hosts were removed
  const setSize = (n: number) => { try { localStorage.setItem(SIZE_KEY, String(n)) } catch { /* ignore */ } setSizeState(n); setPage(0) }
  return (
    <>
      <div className="page-head"><h1>Hosts</h1><small>{hosts.data ? `${total.toLocaleString()} ${q || filter !== 'all' ? 'matching' : 'enrolled'}` : ''}</small></div>
      <div className="toolbar">
        <input placeholder="Search hostname…" value={q} onChange={e => { setQ(e.target.value); setPage(0) }} />
        <select value={filter} onChange={e => { setFilter(e.target.value); setPage(0) }} aria-label="Filter by state">
          <option value="all">All states</option><option value="in-sync">IN SYNC</option><option value="pending">PENDING</option><option value="offline">OFFLINE</option><option value="unknown">UNKNOWN</option>
        </select>
      </div>
      <ErrorBox err={hosts.error} />
      <div className="card flush">
        {!hosts.data ? <Empty>Loading…</Empty> : rows.length === 0 ? <Empty>No hosts match.</Empty> : (
          <div className="table-scroll"><table className="clickable"><thead><tr><th>Host</th><th>State</th><th>Last seen</th><th>Agent</th><th>Problems</th></tr></thead><tbody>
            {rows.map(h => (
              <tr key={h.id} tabIndex={0} onClick={() => setSel(h)} onKeyDown={e => e.key === 'Enter' && setSel(h)}>
                <td><b>{h.hostname}</b></td><td><Badge kind={h.state} /></td><td className="muted">{timeAgo(h.last_seen)}</td>
                <td className="muted">{h.agent_version}</td><td className="wrap small err-text">{h.problems}</td>
              </tr>
            ))}
          </tbody></table></div>
        )}
        {hosts.data && <Pager total={total} page={page} size={size} onPage={setPage} onSize={setSize} />}
      </div>
      {sel && <HostDetail key={sel.id} host={sel} canWrite={canWrite} onClose={() => setSel(null)} onDeleted={() => { setSel(null); hosts.reload(); show('Host removed') }} />}
      {toast}
    </>
  )
}

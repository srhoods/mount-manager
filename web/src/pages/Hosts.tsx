import { useState } from 'react'
import { api, Host } from '../api'
import { Badge, Empty, ErrorBox, fmtTime, hostState, Modal, timeAgo, useLoad, useToast } from '../ui'

function HostDetail({ host, canWrite, onClose, onDeleted }: { host: Host; canWrite: boolean; onClose: () => void; onDeleted: () => void }) {
  const d = useLoad(() => api.host(host.id), 15000)
  const a = useLoad(() => api.audit(host.hostname, 25), 15000)
  const [err, setErr] = useState<string | null>(null)
  const state = new Map((d.data?.state ?? []).map(s => [s.mountpoint, s]))
  const remove = async () => {
    if (!confirm(`Remove ${host.hostname}? It will be able to re-enrol with a valid token.`)) return
    try { await api.deleteHost(host.id); onDeleted() } catch (e) { setErr((e as Error).message) }
  }
  return (
    <Modal title={host.hostname} onClose={onClose} wide>
      <ErrorBox err={err || d.error} />
      <dl className="kv">
        <dt>State</dt><dd><Badge kind={hostState(host)} /></dd>
        <dt>Last seen</dt><dd>{host.last_seen ? `${fmtTime(host.last_seen)} (${timeAgo(host.last_seen)})` : 'never'}</dd>
        <dt>Agent</dt><dd>{host.agent_version || '—'}</dd>
      </dl>
      <h3>Mounts</h3>
      {!d.data ? <Empty>Loading…</Empty> : d.data.desired.length === 0 ? <Empty>No mounts are assigned to this host. Check group membership.</Empty> : (
        <table><thead><tr><th>Mountpoint</th><th>Source</th><th>Type</th><th>Options</th><th>Status</th></tr></thead><tbody>
          {d.data.desired.map(m => {
            const s = state.get(m.mountpoint)
            return <tr key={m.mountpoint}><td className="mono">{m.mountpoint}</td><td className="mono">{m.source}</td><td>{m.fstype}</td><td className="mono">{m.options}</td>
              <td>{s ? <><Badge kind={s.state} />{s.error && <div className="small err-text">{s.error}</div>}</> : <Badge kind="unknown" />}</td></tr>
          })}
        </tbody></table>
      )}
      <h3>Recent events</h3>
      {!a.data ? <Empty>Loading…</Empty> : a.data.length === 0 ? <Empty>No events.</Empty> : (
        <table><tbody>{a.data.map(e => <tr key={e.id}><td className="nowrap muted">{fmtTime(e.ts)}</td><td><b>{e.action}</b></td><td className="wrap muted">{e.detail}</td></tr>)}</tbody></table>
      )}
      {canWrite && <footer><button className="danger" onClick={remove}>Remove host</button></footer>}
    </Modal>
  )
}

export default function Hosts({ canWrite }: { canWrite: boolean }) {
  const hosts = useLoad(api.hosts, 15000)
  const [q, setQ] = useState(''), [filter, setFilter] = useState('all'), [sel, setSel] = useState<Host | null>(null)
  const { toast, show } = useToast()
  const rows = (hosts.data ?? []).filter(h => (filter === 'all' || hostState(h) === filter) && h.hostname.toLowerCase().includes(q.toLowerCase()))
  return (
    <>
      <div className="page-head"><h1>Hosts</h1><small>{hosts.data?.length ?? 0} enrolled</small></div>
      <div className="toolbar">
        <input placeholder="Search hostname…" value={q} onChange={e => setQ(e.target.value)} />
        <select value={filter} onChange={e => setFilter(e.target.value)} aria-label="Filter by state">
          <option value="all">All states</option><option value="in-sync">In sync</option><option value="pending">Pending</option><option value="offline">Offline</option><option value="unknown">Unknown</option>
        </select>
      </div>
      <ErrorBox err={hosts.error} />
      <div className="card flush">
        {!hosts.data ? <Empty>Loading…</Empty> : rows.length === 0 ? <Empty>No hosts match.</Empty> : (
          <table className="clickable"><thead><tr><th>Host</th><th>State</th><th>Last seen</th><th>Agent</th><th>Problems</th></tr></thead><tbody>
            {rows.map(h => (
              <tr key={h.id} tabIndex={0} onClick={() => setSel(h)} onKeyDown={e => e.key === 'Enter' && setSel(h)}>
                <td><b>{h.hostname}</b></td><td><Badge kind={hostState(h)} /></td><td className="muted">{timeAgo(h.last_seen)}</td>
                <td className="muted">{h.agent_version}</td><td className="wrap small err-text">{h.problems}</td>
              </tr>
            ))}
          </tbody></table>
        )}
      </div>
      {sel && <HostDetail host={sel} canWrite={canWrite} onClose={() => setSel(null)} onDeleted={() => { setSel(null); hosts.reload(); show('Host removed') }} />}
      {toast}
    </>
  )
}

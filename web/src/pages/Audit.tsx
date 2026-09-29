import { useState } from 'react'
import { api } from '../api'
import { Empty, ErrorBox, fmtTime, useLoad } from '../ui'

export default function Audit() {
  const [host, setHost] = useState(''), [q, setQ] = useState(''), [limit, setLimit] = useState(200), [live, setLive] = useState(true)
  const a = useLoad(() => api.audit(host, limit), live ? 10000 : 0, [host, limit])
  const rows = (a.data ?? []).filter(e => !q || `${e.actor} ${e.action} ${e.detail}`.toLowerCase().includes(q.toLowerCase()))
  return (
    <>
      <div className="page-head"><h1>Audit log</h1><small>Admin actions and agent mount actions</small></div>
      <div className="toolbar">
        <input placeholder="Filter by host…" value={host} onChange={e => setHost(e.target.value)} />
        <input placeholder="Search text…" value={q} onChange={e => setQ(e.target.value)} />
        <select value={limit} onChange={e => setLimit(parseInt(e.target.value))} aria-label="Rows"><option value={100}>100</option><option value={200}>200</option><option value={500}>500</option><option value={1000}>1000</option></select>
        <label className="check inline"><input type="checkbox" checked={live} onChange={e => setLive(e.target.checked)} />Live</label>
      </div>
      <ErrorBox err={a.error} />
      <div className="card flush">
        {!a.data ? <Empty>Loading…</Empty> : rows.length === 0 ? <Empty>No events.</Empty> : (
          <table><thead><tr><th>Time</th><th>Actor</th><th>Host</th><th>Action</th><th>Detail</th></tr></thead><tbody>
            {rows.map(e => <tr key={e.id}><td className="nowrap muted">{fmtTime(e.ts)}</td><td>{e.actor}</td><td>{e.host || <span className="muted">—</span>}</td><td><b>{e.action}</b></td><td className="wrap muted">{e.detail}</td></tr>)}
          </tbody></table>
        )}
      </div>
    </>
  )
}

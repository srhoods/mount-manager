import { api } from '../api'
import { Badge, Empty, ErrorBox, fmtTime, timeAgo, useLoad } from '../ui'

export default function Dashboard() {
  const summary = useLoad(api.hostSummary, 30000)
  const attention = useLoad(() => api.hosts({ state: 'attention', limit: 15 }), 30000)
  const audit = useLoad(() => api.audit('', 12), 30000)
  const sm = summary.data
  const tiles: [string, number, string][] = [
    ['Hosts', sm?.total ?? 0, ''], ['In sync', sm?.in_sync ?? 0, 'ok'], ['Pending changes', sm?.pending ?? 0, 'warn'], ['Offline', sm?.offline ?? 0, 'bad'],
  ]
  return (
    <>
      <div className="page-head"><h1>Dashboard</h1><small>Refreshes every 30s</small></div>
      <ErrorBox err={summary.error || attention.error} />
      <div className="tiles">{tiles.map(([l, n, k]) => <div key={l} className={`tile ${k}`}><span>{l}</span><b>{n}</b></div>)}</div>
      <div className="cols">
        <section className="card">
          <h3>Needs attention</h3>
          {!attention.data ? <Empty>Loading…</Empty> : attention.data.items.length === 0 ? <Empty>All hosts are in sync.</Empty> : (
            <table><thead><tr><th>Host</th><th>State</th><th>Detail</th></tr></thead><tbody>
              {attention.data.items.map(h => (
                <tr key={h.id}><td className="nowrap"><a href="#/hosts">{h.hostname}</a></td><td><Badge kind={h.state} /></td>
                  <td className="muted wrap">{h.state === 'offline' ? `Last seen ${timeAgo(h.last_seen)}` : h.problems || '—'}</td></tr>
              ))}
            </tbody></table>
          )}
          {attention.data && attention.data.total > attention.data.items.length && <p className="muted small more"><a href="#/hosts">{(attention.data.total - attention.data.items.length).toLocaleString()} more…</a></p>}
        </section>
        <section className="card">
          <h3>Recent activity</h3>
          {!audit.data ? <Empty>Loading…</Empty> : audit.data.length === 0 ? <Empty>No activity yet.</Empty> : (
            <ul className="feed">{audit.data.map(a => (
              <li key={a.id}><b>{a.action}</b> <span className="muted">{a.host || a.actor}</span><div className="muted small">{a.detail} · {fmtTime(a.ts)}</div></li>
            ))}</ul>
          )}
        </section>
      </div>
    </>
  )
}

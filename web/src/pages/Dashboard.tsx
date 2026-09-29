import { api } from '../api'
import { Badge, Empty, ErrorBox, fmtTime, hostState, timeAgo, useLoad } from '../ui'

export default function Dashboard() {
  const hosts = useLoad(api.hosts, 30000)
  const audit = useLoad(() => api.audit('', 12), 30000)
  const hs = hosts.data ?? []
  const count = (s: string) => hs.filter(h => hostState(h) === s).length
  const attention = hs.filter(h => hostState(h) !== 'in-sync')
  const tiles: [string, number, string][] = [
    ['Hosts', hs.length, ''], ['In sync', count('in-sync'), 'ok'], ['Pending changes', count('pending'), 'warn'], ['Offline', count('offline'), 'bad'],
  ]
  return (
    <>
      <div className="page-head"><h1>Dashboard</h1><small>Refreshes every 30s</small></div>
      <ErrorBox err={hosts.error} />
      <div className="tiles">{tiles.map(([l, n, k]) => <div key={l} className={`tile ${k}`}><span>{l}</span><b>{n}</b></div>)}</div>
      <div className="cols">
        <section className="card">
          <h3>Needs attention</h3>
          {!hosts.data ? <Empty>Loading…</Empty> : attention.length === 0 ? <Empty>All hosts are in sync.</Empty> : (
            <table><thead><tr><th>Host</th><th>State</th><th>Detail</th></tr></thead><tbody>
              {attention.slice(0, 15).map(h => (
                <tr key={h.id}><td><a href="#/hosts">{h.hostname}</a></td><td><Badge kind={hostState(h)} /></td>
                  <td className="muted wrap">{hostState(h) === 'offline' ? `Last seen ${timeAgo(h.last_seen)}` : h.problems || '—'}</td></tr>
              ))}
            </tbody></table>
          )}
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

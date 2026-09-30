import { FormEvent, useState } from 'react'
import { api, EnrolToken } from '../api'
import { Badge, Empty, ErrorBox, duration, fmtTime, useLoad, useToast } from '../ui'

function Tokens({ reloadKey, onChanged }: { reloadKey: number; onChanged: () => void }) {
  const [all, setAll] = useState(false)
  const t = useLoad(() => api.tokens(all), 20000, [all, reloadKey])
  const { toast, show } = useToast()
  const revoke = async (x: EnrolToken) => {
    const what = x.note ? `“${x.note}”` : `#${x.id}`
    if (!confirm(`Revoke enrolment token ${what}? Its ${x.uses_left} remaining use${x.uses_left === 1 ? '' : 's'} will be lost. Hosts already enrolled with it are not affected.`)) return
    try { await api.revokeToken(x.id); show('Token revoked'); t.reload(); onChanged() } catch (e) { show((e as Error).message) }
  }
  const rows = t.data ?? []
  return (
    <section>
      <div className="page-head sub">
        <h2>{all ? 'Enrolment tokens' : 'Active enrolment tokens'}</h2>
        <label className="check inline"><input type="checkbox" checked={all} onChange={e => setAll(e.target.checked)} />Include expired, used up and revoked</label>
      </div>
      <ErrorBox err={t.error} />
      <div className="card flush">
        {!t.data ? <Empty>Loading…</Empty> : rows.length === 0 ? <Empty>{all ? 'No enrolment tokens.' : 'No active enrolment tokens. Hosts cannot enrol until you generate one.'}</Empty> : (
          <table><thead><tr><th>Token</th><th>Status</th><th>Uses remaining</th><th>Valid for</th><th>Expires</th><th>Created</th><th /></tr></thead><tbody>
            {rows.map(x => {
              const soon = x.status === 'active' && x.seconds_left < 3600
              return (
                <tr key={x.id} className={x.status === 'active' ? '' : 'dim'}>
                  <td><b>{x.note || <span className="muted">(no note)</span>}</b><div className="muted small">#{x.id}</div></td>
                  <td><Badge kind={x.status} /></td>
                  <td>
                    <div className="uses"><span>{x.uses_left} of {x.uses_total}</span>
                      <span className="bar" aria-hidden="true"><i style={{ width: `${x.uses_total ? Math.round((x.uses_left / x.uses_total) * 100) : 0}%` }} /></span></div>
                  </td>
                  <td className={soon ? 'warn-text' : ''}>{x.status === 'active' ? duration(x.seconds_left) : <span className="muted">—</span>}</td>
                  <td className="nowrap muted">{fmtTime(x.expires)}</td>
                  <td className="nowrap muted">{fmtTime(x.created_at)}{x.created_by && <div className="small">by {x.created_by}</div>}</td>
                  <td className="actions">{x.status === 'active' && <button className="danger" onClick={() => revoke(x)}>Revoke</button>}</td>
                </tr>
              )
            })}
          </tbody></table>
        )}
      </div>
      {toast}
    </section>
  )
}

export default function Enrol({ canWrite }: { canWrite: boolean }) {
  const [note, setNote] = useState(''), [hours, setHours] = useState(24), [uses, setUses] = useState(1)
  const [token, setToken] = useState<string | null>(null), [err, setErr] = useState<string | null>(null), [copied, setCopied] = useState(false)
  const [reloadKey, setReloadKey] = useState(0)
  const submit = async (e: FormEvent) => {
    e.preventDefault(); setErr(null); setCopied(false)
    try { setToken((await api.createToken(note, hours, uses)).token); setReloadKey(k => k + 1) } catch (x) { setErr((x as Error).message) }
  }
  const server = location.hostname
  const snippet = token && `# on the new host (Rocky 9+)
dnf install -y mountmgr-agent
# set 'server=' in /etc/mountmgr/agent.conf to https://${server}:8443
install -o mountmgr -g mountmgr -m 0600 /dev/null /var/lib/mountmgr/enroll.token
echo '${token}' > /var/lib/mountmgr/enroll.token
systemctl enable --now mountmgr-agent`
  return (
    <>
      <div className="page-head"><h1>Enrolment</h1></div>
      <p className="muted">Enrolment tokens let new hosts obtain a client certificate. Once enrolled, a host joins any group whose hostname regex matches it.</p>
      {!canWrite ? <div className="card">Only administrators can manage enrolment tokens.</div> : (
        <>
          <form className="card narrow" onSubmit={submit}>
            <label>Note<input placeholder="e.g. Render farm batch 3" value={note} onChange={e => setNote(e.target.value)} /></label>
            <div className="row2">
              <label>Valid for (hours)<input type="number" min={1} value={hours} onChange={e => setHours(parseInt(e.target.value) || 1)} /></label>
              <label>Maximum uses<input type="number" min={1} value={uses} onChange={e => setUses(parseInt(e.target.value) || 1)} /></label>
            </div>
            <ErrorBox err={err} />
            <button className="primary">Generate token</button>
          </form>
          {token && (
            <div className="card">
              <h3>Token created</h3>
              <p className="warn-text small">This token is shown only once. Store it securely.</p>
              <pre>{snippet}</pre>
              <button onClick={() => { navigator.clipboard?.writeText(snippet!).then(() => setCopied(true)) }}>{copied ? 'Copied' : 'Copy instructions'}</button>
            </div>
          )}
          <Tokens reloadKey={reloadKey} onChanged={() => setReloadKey(k => k + 1)} />
        </>
      )}
    </>
  )
}

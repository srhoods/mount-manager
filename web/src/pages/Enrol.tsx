import { FormEvent, useState } from 'react'
import { api } from '../api'
import { ErrorBox } from '../ui'

export default function Enrol({ canWrite }: { canWrite: boolean }) {
  const [note, setNote] = useState(''), [hours, setHours] = useState(24), [uses, setUses] = useState(1)
  const [token, setToken] = useState<string | null>(null), [err, setErr] = useState<string | null>(null), [copied, setCopied] = useState(false)
  const submit = async (e: FormEvent) => {
    e.preventDefault(); setErr(null); setCopied(false)
    try { setToken((await api.createToken(note, hours, uses)).token) } catch (x) { setErr((x as Error).message) }
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
      {!canWrite ? <div className="card">Only administrators can create enrolment tokens.</div> : (
        <form className="card narrow" onSubmit={submit}>
          <label>Note<input placeholder="e.g. Render farm batch 3" value={note} onChange={e => setNote(e.target.value)} /></label>
          <div className="row2">
            <label>Valid for (hours)<input type="number" min={1} value={hours} onChange={e => setHours(parseInt(e.target.value) || 1)} /></label>
            <label>Maximum uses<input type="number" min={1} value={uses} onChange={e => setUses(parseInt(e.target.value) || 1)} /></label>
          </div>
          <ErrorBox err={err} />
          <button className="primary">Generate token</button>
        </form>
      )}
      {token && (
        <div className="card">
          <h3>Token created</h3>
          <p className="warn-text small">This token is shown only once. Store it securely.</p>
          <pre>{snippet}</pre>
          <button onClick={() => { navigator.clipboard?.writeText(snippet!).then(() => setCopied(true)) }}>{copied ? 'Copied' : 'Copy instructions'}</button>
        </div>
      )}
    </>
  )
}

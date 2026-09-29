import { FormEvent, useState } from 'react'
import { api, Template } from '../api'
import { Empty, ErrorBox, Modal, useLoad, useToast } from '../ui'

const blank = { name: '', fstype: 'nfs', source: '', mountpoint: '', options: 'rw,_netdev,hard' }

function Editor({ initial, onClose, onSaved }: { initial: Template | null; onClose: () => void; onSaved: () => void }) {
  const [f, setF] = useState(initial ? { name: initial.name, fstype: initial.fstype, source: initial.source, mountpoint: initial.mountpoint, options: initial.options } : blank)
  const [err, setErr] = useState<string | null>(null), [busy, setBusy] = useState(false)
  const set = (k: keyof typeof f) => (e: { target: { value: string } }) => setF({ ...f, [k]: e.target.value })
  const submit = async (e: FormEvent) => {
    e.preventDefault(); setBusy(true); setErr(null)
    try { await api.saveTemplate(f); onSaved() } catch (x) { setErr((x as Error).message) } finally { setBusy(false) }
  }
  return (
    <Modal title={initial ? `Edit template “${initial.name}”` : 'New template'} onClose={onClose}>
      <form onSubmit={submit}>
        <label>Name<input value={f.name} onChange={set('name')} disabled={!!initial} required /></label>
        <label>Type<select value={f.fstype} onChange={set('fstype')}><option value="nfs">nfs</option><option value="nfs4">nfs4</option><option value="wekafs">wekafs</option></select></label>
        <label>Source<input className="mono" placeholder={f.fstype === 'wekafs' ? 'backend/filesystem  e.g. weka01/fs1' : 'server:/export/path'} value={f.source} onChange={set('source')} required /></label>
        <label>Mountpoint<input className="mono" placeholder="/mnt/data, /sqpc/projects" value={f.mountpoint} onChange={set('mountpoint')} required pattern="/.*" /></label>
        <label>Options<input className="mono" value={f.options} onChange={set('options')} /></label>
        {initial && <p className="hint">Saving changes to an existing template rolls the update out to every host using it: the agent unmounts and remounts. Busy mounts are reported as pending and retried.</p>}
        <ErrorBox err={err} />
        <footer><button type="button" onClick={onClose}>Cancel</button><button className="primary" disabled={busy}>{busy ? 'Saving…' : 'Save'}</button></footer>
      </form>
    </Modal>
  )
}

export default function Templates({ canWrite }: { canWrite: boolean }) {
  const t = useLoad(api.templates)
  const [edit, setEdit] = useState<Template | null | 'new'>(null)
  const { toast, show } = useToast()
  const del = async (x: Template) => {
    if (!confirm(`Delete template “${x.name}”? Hosts will unmount it.`)) return
    try { await api.deleteTemplate(x.id); t.reload(); show('Template deleted') } catch (e) { show((e as Error).message) }
  }
  return (
    <>
      <div className="page-head"><h1>Templates</h1>{canWrite && <button className="primary" onClick={() => setEdit('new')}>New template</button>}</div>
      <ErrorBox err={t.error} />
      <div className="card flush">
        {!t.data ? <Empty>Loading…</Empty> : t.data.length === 0 ? <Empty>No templates yet. A template defines one mount; attach it to groups to deploy it.</Empty> : (
          <table><thead><tr><th>Name</th><th>Type</th><th>Source</th><th>Mountpoint</th><th>Options</th><th>Ver</th>{canWrite && <th />}</tr></thead><tbody>
            {t.data.map(x => (
              <tr key={x.id}><td><b>{x.name}</b></td><td>{x.fstype}</td><td className="mono">{x.source}</td><td className="mono">{x.mountpoint}</td><td className="mono">{x.options}</td><td className="muted">{x.version}</td>
                {canWrite && <td className="actions"><button onClick={() => setEdit(x)}>Edit</button><button className="danger" onClick={() => del(x)}>Delete</button></td>}</tr>
            ))}
          </tbody></table>
        )}
      </div>
      {edit && <Editor initial={edit === 'new' ? null : edit} onClose={() => setEdit(null)} onSaved={() => { setEdit(null); t.reload(); show('Template saved') }} />}
      {toast}
    </>
  )
}

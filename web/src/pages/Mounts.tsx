import { FormEvent, useEffect, useState } from 'react'
import { api, Template } from '../api'
import { Empty, ErrorBox, Modal, Pager, useLoad, usePageSize, useToast } from '../ui'

const blank = { name: '', fstype: 'nfs', source: '', mountpoint: '', options: 'rw,_netdev,hard' }

function Editor({ initial, existing, onClose, onSaved }: { initial: Template | null; existing: string[]; onClose: () => void; onSaved: () => void }) {
  const [f, setF] = useState(initial ? { name: initial.name, fstype: initial.fstype, source: initial.source, mountpoint: initial.mountpoint, options: initial.options } : blank)
  const [err, setErr] = useState<string | null>(null), [busy, setBusy] = useState(false)
  const set = (k: keyof typeof f) => (e: { target: { value: string } }) => setF({ ...f, [k]: e.target.value })
  const submit = async (e: FormEvent) => {
    e.preventDefault(); setErr(null)
    // saving under an existing name updates that mount, which is right for Edit but never what "New" means
    if (!initial && existing.some(n => n.toLowerCase() === f.name.trim().toLowerCase())) {
      setErr(`A mount named “${f.name.trim()}” already exists. Use Edit to change it, or Clone to copy it.`); return
    }
    setBusy(true)
    try { await api.saveTemplate({ ...f, name: f.name.trim() }); onSaved() } catch (x) { setErr((x as Error).message) } finally { setBusy(false) }
  }
  return (
    <Modal title={initial ? `Edit mount “${initial.name}”` : 'New mount'} onClose={onClose}>
      <form onSubmit={submit}>
        <label>Name<input value={f.name} onChange={set('name')} disabled={!!initial} required /></label>
        <label>Type<select value={f.fstype} onChange={set('fstype')}><option value="nfs">nfs</option><option value="nfs4">nfs4</option><option value="wekafs">wekafs</option></select></label>
        <label>Source<input className="mono" placeholder={f.fstype === 'wekafs' ? 'backend/filesystem  e.g. weka01/fs1' : 'server:/export/path'} value={f.source} onChange={set('source')} required /></label>
        {f.fstype === 'wekafs' && <p className="hint">The first Weka mount on a host compiles the client driver and starts the Weka container, so the agent allows up to 120 seconds for it.</p>}
        <label>Mountpoint<input className="mono" placeholder="/mnt/data, /sqpc/projects" value={f.mountpoint} onChange={set('mountpoint')} required pattern="/.*" /></label>
        <label>Options<input className="mono" value={f.options} onChange={set('options')} /></label>
        {initial && <p className="hint">Saving changes to an existing mount rolls the update out to every host using it: the agent unmounts and remounts. Busy mounts are reported as pending and retried.</p>}
        <ErrorBox err={err} />
        <footer><button type="button" onClick={onClose}>Cancel</button><button className="primary" disabled={busy}>{busy ? 'Saving…' : 'Save'}</button></footer>
      </form>
    </Modal>
  )
}

function CloneDialog({ src, onClose, onCloned }: { src: Template; onClose: () => void; onCloned: (name: string) => void }) {
  const [name, setName] = useState(`${src.name}-copy`)
  const [err, setErr] = useState<string | null>(null), [busy, setBusy] = useState(false)
  const submit = async (e: FormEvent) => {
    e.preventDefault(); setBusy(true); setErr(null)
    try { await api.cloneTemplate(src.id, name.trim()); onCloned(name.trim()) } catch (x) { setErr((x as Error).message) } finally { setBusy(false) }
  }
  return (
    <Modal title={`Clone mount “${src.name}”`} onClose={onClose}>
      <form onSubmit={submit}>
        <label>Name of the new mount<input autoFocus value={name} onChange={e => setName(e.target.value)} onFocus={e => e.target.select()} required /></label>
        <dl className="facts compact">
          <div><dt>Type</dt><dd>{src.fstype}</dd></div>
          <div><dt>Source</dt><dd className="mono">{src.source}</dd></div>
          <div><dt>Mountpoint</dt><dd className="mono">{src.mountpoint}</dd></div>
          <div><dt>Options</dt><dd className="mono">{src.options || '—'}</dd></div>
        </dl>
        <p className="hint">The copy has the same type, source, mountpoint and options. It is not added to any group; change anything you need with Edit afterwards.</p>
        <ErrorBox err={err} />
        <footer><button type="button" onClick={onClose}>Cancel</button><button className="primary" disabled={busy || !name.trim()}>{busy ? 'Cloning…' : 'Clone'}</button></footer>
      </form>
    </Modal>
  )
}

interface Tip { x: number; y: number; m: Template }

const TYPES = ['nfs', 'nfs4', 'wekafs']

export default function Mounts({ canWrite }: { canWrite: boolean }) {
  const [fName, setFName] = useState(''), [fSource, setFSource] = useState(''), [fMount, setFMount] = useState(''), [fType, setFType] = useState('')
  const [size, setSizeRaw] = usePageSize('mm.mounts.pageSize'), [page, setPage] = useState(0)
  const t = useLoad(() => api.mounts({ name: fName, source: fSource, mountpoint: fMount, type: fType, limit: size, offset: page * size }), 0, [fName, fSource, fMount, fType, size, page])
  const all = useLoad(api.templates)   // every name, so New can refuse a name that already exists
  const [edit, setEdit] = useState<Template | null | 'new'>(null)
  const [clone, setClone] = useState<Template | null>(null)
  const [tip, setTip] = useState<Tip | null>(null)
  const { toast, show } = useToast()
  const total = t.data?.total ?? 0
  const rows = t.data?.items ?? []
  const filtered = !!(fName || fSource || fMount || fType)
  const lastPage = Math.max(0, Math.ceil(total / size) - 1)
  useEffect(() => { if (t.data && page > lastPage) setPage(lastPage) }, [t.data, page, lastPage])   // e.g. after deleting the last row of a page
  const reload = () => { t.reload(); all.reload() }
  const filter = (set: (v: string) => void) => (e: { target: { value: string } }) => { set(e.target.value); setPage(0) }
  const clear = () => { setFName(''); setFSource(''); setFMount(''); setFType(''); setPage(0) }
  const del = async (x: Template) => {
    if (!confirm(`Delete mount “${x.name}”? Hosts will unmount it.`)) return
    try { await api.deleteTemplate(x.id); reload(); show('Mount deleted') } catch (e) { show((e as Error).message) }
  }
  // options are shown on hover or keyboard focus instead of taking a column
  const showTip = (e: React.MouseEvent | React.FocusEvent, m: Template) => {
    const r = (e.currentTarget as HTMLElement).getBoundingClientRect()
    const mouse = 'clientX' in e
    setTip({ x: Math.min(mouse ? e.clientX + 16 : r.left + 24, window.innerWidth - 440), y: mouse ? e.clientY + 18 : r.bottom + 4, m })
  }
  return (
    <>
      <div className="page-head"><h1>Mounts</h1>
        <span className="head-right">{t.data && <small>{total.toLocaleString()} {filtered ? 'matching' : 'mounts'}</small>}{canWrite && <button className="primary" onClick={() => setEdit('new')}>New mount</button>}</span></div>
      <div className="toolbar filters">
        <input placeholder="Search name…" aria-label="Search by name" value={fName} onChange={filter(setFName)} />
        <input placeholder="Search source…" aria-label="Search by source" value={fSource} onChange={filter(setFSource)} />
        <input placeholder="Search mountpoint…" aria-label="Search by mountpoint" value={fMount} onChange={filter(setFMount)} />
        <select value={fType} onChange={filter(setFType)} aria-label="Filter by type">
          <option value="">All types</option>{TYPES.map(x => <option key={x} value={x}>{x}</option>)}
        </select>
        {filtered && <button onClick={clear}>Clear filters</button>}
      </div>
      <ErrorBox err={t.error} />
      <div className="card flush">
        {!t.data ? <Empty>Loading…</Empty> : rows.length === 0 ? <Empty>{filtered ? 'No mounts match these filters.' : 'No mounts yet. A mount defines what to mount and where; attach it to groups to deploy it.'}</Empty> : (
          <div className="table-scroll"><table className="clickable"><thead><tr><th>Name</th><th>Type</th><th>Source</th><th>Mountpoint</th><th>Ver</th>{canWrite && <th />}</tr></thead><tbody>
            {rows.map(x => (
              <tr key={x.id} tabIndex={0} onMouseEnter={e => showTip(e, x)} onMouseMove={e => showTip(e, x)} onMouseLeave={() => setTip(null)} onFocus={e => showTip(e, x)} onBlur={() => setTip(null)}>
                <td><b>{x.name}</b></td><td>{x.fstype}</td><td className="mono">{x.source}</td><td className="mono">{x.mountpoint}</td><td className="muted">{x.version}</td>
                {canWrite && <td className="actions"><button onClick={() => setClone(x)}>Clone</button><button onClick={() => setEdit(x)}>Edit</button><button className="danger" onClick={() => del(x)}>Delete</button></td>}</tr>
            ))}
          </tbody></table></div>
        )}
        {t.data && <Pager total={total} page={page} size={size} noun="mounts" onPage={setPage} onSize={n => { setSizeRaw(n); setPage(0) }} />}
      </div>
      {tip && <div className="tip" role="tooltip" style={{ left: tip.x, top: tip.y }}><span className="muted">Options</span> <span className="mono">{tip.m.options || '(none)'}</span></div>}
      {edit && <Editor initial={edit === 'new' ? null : edit} existing={(all.data ?? []).map(x => x.name)} onClose={() => setEdit(null)} onSaved={() => { setEdit(null); reload(); show('Mount saved') }} />}
      {clone && <CloneDialog src={clone} onClose={() => setClone(null)} onCloned={name => { setClone(null); reload(); show(`Cloned as “${name}”`) }} />}
      {toast}
    </>
  )
}

import { FormEvent, useMemo, useState } from 'react'
import { api, Group, HostBrief, Template } from '../api'
import { Empty, ErrorBox, Modal, useLoad, useToast } from '../ui'

function compile(rx: string): RegExp | null | 'invalid' {
  if (!rx) return null
  try { return new RegExp(rx) } catch { return 'invalid' }
}

function Editor({ initial, groups, templates, hosts, onClose, onSaved }: {
  initial: Group | null; groups: Group[]; templates: Template[]; hosts: HostBrief[]; onClose: () => void; onSaved: () => void
}) {
  const [name, setName] = useState(initial?.name ?? '')
  const [prio, setPrio] = useState(initial?.priority ?? 100)
  const [rx, setRx] = useState(initial?.host_regex ?? '')
  const [tpl, setTpl] = useState(new Set(initial?.templates ?? []))
  const [mem, setMem] = useState(new Set(initial?.members ?? []))
  const [q, setQ] = useState('')
  const [err, setErr] = useState<string | null>(null), [busy, setBusy] = useState(false)
  const re = compile(rx)
  const matches = useMemo(() => re && re !== 'invalid' ? hosts.filter(h => re.test(h.hostname)) : [], [re, hosts])
  const toggle = (s: Set<string>, set: (x: Set<string>) => void, v: string) => { const n = new Set(s); n.has(v) ? n.delete(v) : n.add(v); set(n) }

  // Mountpoint conflicts within this group: two selected mounts sharing a mountpoint.
  const conflicts = useMemo(() => {
    const seen = new Map<string, string>(); const out: string[] = []
    templates.filter(t => tpl.has(t.name)).forEach(t => { const o = seen.get(t.mountpoint); if (o) out.push(`${t.mountpoint} (${o}, ${t.name})`); else seen.set(t.mountpoint, t.name) })
    return out
  }, [tpl, templates])
  const overlap = useMemo(() => {
    const mine = new Set(templates.filter(t => tpl.has(t.name)).map(t => t.mountpoint))
    return groups.filter(g => g.id !== initial?.id && g.priority === prio && g.templates.some(n => mine.has(templates.find(t => t.name === n)?.mountpoint ?? '')))
  }, [groups, templates, tpl, prio, initial])

  const submit = async (e: FormEvent) => {
    e.preventDefault(); setBusy(true); setErr(null)
    try {
      const { id } = await api.saveGroup({ name, priority: prio, host_regex: rx })
      const before = { t: new Set(initial?.templates ?? []), m: new Set(initial?.members ?? []) }
      for (const t of templates) { const had = before.t.has(t.name), want = tpl.has(t.name); if (had !== want) await api.link(want, id, 'templates', t.id) }
      for (const h of hosts) { const had = before.m.has(h.hostname), want = mem.has(h.hostname); if (had !== want) await api.link(want, id, 'members', h.id) }
      onSaved()
    } catch (x) { setErr((x as Error).message) } finally { setBusy(false) }
  }
  const shownHosts = hosts.filter(h => h.hostname.toLowerCase().includes(q.toLowerCase()))
  return (
    <Modal title={initial ? `Edit group “${initial.name}”` : 'New group'} onClose={onClose} wide>
      <form onSubmit={submit}>
        <div className="row2">
          <label>Name<input value={name} onChange={e => setName(e.target.value)} disabled={!!initial} required /></label>
          <label>Priority<input type="number" value={prio} onChange={e => setPrio(parseInt(e.target.value) || 0)} />
            <small className="hint">Higher wins when groups define the same mountpoint.</small></label>
        </div>
        <label>Dynamic membership (hostname regex)
          <input className="mono" placeholder="^web-\d+\.example\.com$" value={rx} onChange={e => setRx(e.target.value)} />
        </label>
        {re === 'invalid' ? <p className="err-text small">Invalid regular expression.</p>
          : re ? <p className="hint">Matches {matches.length} enrolled host{matches.length === 1 ? '' : 's'}{matches.length ? `: ${matches.slice(0, 6).map(h => h.hostname).join(', ')}${matches.length > 6 ? '…' : ''}` : ''}. Hosts that enrol later are added automatically.</p> : null}
        <h3>Mounts</h3>
        {templates.length === 0 ? <Empty>Create a mount first.</Empty> : (
          <div className="checks">{templates.map(t => (
            <label key={t.id} className="check"><input type="checkbox" checked={tpl.has(t.name)} onChange={() => toggle(tpl, setTpl, t.name)} />
              <b>{t.name}</b><span className="muted mono small">{t.mountpoint}</span></label>
          ))}</div>
        )}
        {conflicts.length > 0 && <p className="err-text small">Mounts in this group share a mountpoint: {conflicts.join('; ')}. Only one will apply.</p>}
        {overlap.length > 0 && <p className="warn-text small">Same priority as {overlap.map(g => g.name).join(', ')} with an overlapping mountpoint — the winner is not well-defined. Use different priorities.</p>}
        <h3>Static members <small className="muted">({mem.size})</small></h3>
        <input placeholder="Filter hosts…" value={q} onChange={e => setQ(e.target.value)} />
        <div className="checks scroll">{shownHosts.length === 0 ? <Empty>No hosts.</Empty> : shownHosts.map(h => (
          <label key={h.id} className="check"><input type="checkbox" checked={mem.has(h.hostname)} onChange={() => toggle(mem, setMem, h.hostname)} />{h.hostname}
            {re && re !== 'invalid' && re.test(h.hostname) && <span className="muted small">(also matches regex)</span>}</label>
        ))}</div>
        <ErrorBox err={err} />
        <footer><button type="button" onClick={onClose}>Cancel</button><button className="primary" disabled={busy || !name || re === 'invalid'}>{busy ? 'Saving…' : 'Save'}</button></footer>
      </form>
    </Modal>
  )
}

export default function Groups({ canWrite }: { canWrite: boolean }) {
  const g = useLoad(api.groups)
  const t = useLoad(api.templates)
  const h = useLoad(api.hostNames)
  const [edit, setEdit] = useState<Group | null | 'new'>(null)
  const { toast, show } = useToast()
  const del = async (x: Group) => {
    if (!confirm(`Delete group “${x.name}”? Its mounts will be removed from member hosts.`)) return
    try { await api.deleteGroup(x.id); g.reload(); show('Group deleted') } catch (e) { show((e as Error).message) }
  }
  const ready = g.data && t.data && h.data
  return (
    <>
      <div className="page-head"><h1>Groups</h1>{canWrite && <button className="primary" disabled={!ready} onClick={() => setEdit('new')}>New group</button>}</div>
      <ErrorBox err={g.error || t.error || h.error} />
      <div className="card flush">
        {!g.data ? <Empty>Loading…</Empty> : g.data.length === 0 ? <Empty>No groups yet. Groups attach mounts to hosts, statically or by hostname regex.</Empty> : (
          <table><thead><tr><th>Name</th><th>Priority</th><th>Regex</th><th>Mounts</th><th>Static members</th>{canWrite && <th />}</tr></thead><tbody>
            {g.data.map(x => (
              <tr key={x.id}><td><b>{x.name}</b></td><td>{x.priority}</td><td className="mono">{x.host_regex || <span className="muted">—</span>}</td>
                <td>{x.templates.join(', ') || <span className="muted">—</span>}</td><td className="muted">{x.members.length}</td>
                {canWrite && <td className="actions"><button onClick={() => setEdit(x)} disabled={!ready}>Edit</button><button className="danger" onClick={() => del(x)}>Delete</button></td>}</tr>
            ))}
          </tbody></table>
        )}
      </div>
      {edit && ready && <Editor initial={edit === 'new' ? null : edit} groups={g.data!} templates={t.data!} hosts={h.data!} onClose={() => setEdit(null)}
        onSaved={() => { setEdit(null); g.reload(); show('Group saved') }} />}
      {toast}
    </>
  )
}

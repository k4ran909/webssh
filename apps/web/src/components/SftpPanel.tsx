import { useCallback, useEffect, useRef, useState, type DragEvent } from 'react'
import { api, ApiError } from '../lib/api'
import { MAX_EDIT_BYTES } from '../lib/limits'
import { liveSessions } from '../lib/live'
import { Icon } from './icons'

interface Entry {
  name: string
  size: number
  mode: string
  perm: string
  isDir: boolean
  isLink?: boolean
  mtime: number
}

interface Transfer {
  id: string
  name: string
  dir: string
  file: File
  loaded: number
  total: number
  state: 'active' | 'done' | 'error' | 'exists'
  error?: string
}

const join = (dir: string, name: string) => (dir.endsWith('/') ? dir + name : `${dir}/${name}`)
const parent = (p: string) => (p === '/' ? '/' : p.replace(/\/[^/]+\/?$/, '') || '/')
const quote = (p: string) => `'${p.replace(/'/g, `'\\''`)}'`
const fmtSize = (n: number) => (n < 1024 ? `${n} B` : n < 1 << 20 ? `${(n / 1024).toFixed(1)} K` : n < 1 << 30 ? `${(n / (1 << 20)).toFixed(1)} M` : `${(n / (1 << 30)).toFixed(1)} G`)
function fmtTime(t: number) {
  const d = new Date(t * 1000)
  const now = new Date()
  return d.getFullYear() === now.getFullYear()
    ? d.toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })
    : d.toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' })
}
const message = (e: unknown) => (e instanceof Error ? e.message : 'Something went wrong.')

interface Props {
  handle: string
  sessionId: string
  onClose: () => void
}

export function SftpPanel({ handle, sessionId, onClose }: Props) {
  const base = `/api/sftp/${encodeURIComponent(handle)}`
  const [path, setPath] = useState('')
  const [entries, setEntries] = useState<Entry[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [editPath, setEditPath] = useState<string | null>(null)
  const [pathInput, setPathInput] = useState('')
  const [rowEdit, setRowEdit] = useState<{ name: string; kind: 'rename' | 'chmod' | 'delete'; value: string } | null>(null)
  const [newFolder, setNewFolder] = useState<string | null>(null)
  const [transfers, setTransfers] = useState<Transfer[]>([])
  const [drag, setDrag] = useState(false)
  const [editor, setEditor] = useState<{ path: string; text: string; original: string } | null>(null)
  const fileInput = useRef<HTMLInputElement>(null)
  const pathRef = useRef(path)
  pathRef.current = path

  const load = useCallback(async (p: string) => {
    setLoading(true)
    setError('')
    try {
      const r = await api<{ path: string; entries: Entry[] }>('GET', `${base}/ls?path=${encodeURIComponent(p)}`)
      setPath(r.path)
      setPathInput(r.path)
      setEntries(r.entries)
      setRowEdit(null)
    } catch (e) {
      setError(message(e))
    } finally {
      setLoading(false)
    }
  }, [base])

  useEffect(() => { load('') }, [load])

  const op = async (body: Record<string, string>) => {
    try {
      await api('POST', `${base}/op`, body)
      await load(pathRef.current)
    } catch (e) {
      setError(message(e))
    }
  }

  const download = (name: string) => {
    const a = document.createElement('a')
    a.href = `${base}/file?download=1&path=${encodeURIComponent(join(path, name))}`
    a.download = name
    a.click()
  }

  // Uploads use XHR for progress events; fetch can't report upload progress.
  const upload = (t: Transfer, overwrite = false) => {
    const xhr = new XMLHttpRequest()
    xhr.open('PUT', `${base}/file?path=${encodeURIComponent(join(t.dir, t.name))}${overwrite ? '&overwrite=1' : ''}`)
    const patch = (p: Partial<Transfer>) => setTransfers((ts) => ts.map((x) => (x.id === t.id ? { ...x, ...p } : x)))
    xhr.upload.onprogress = (e) => patch({ loaded: e.loaded, total: e.total || t.total })
    xhr.onload = () => {
      if (xhr.status >= 200 && xhr.status < 300) {
        patch({ state: 'done', loaded: t.total })
        if (t.dir === pathRef.current) load(pathRef.current)
        setTimeout(() => setTransfers((ts) => ts.filter((x) => x.id !== t.id)), 2500)
      } else {
        let msg = xhr.statusText
        try { msg = JSON.parse(xhr.responseText).msg ?? msg } catch { /* not JSON */ }
        patch({ state: xhr.status === 409 ? 'exists' : 'error', error: msg })
      }
    }
    xhr.onerror = () => patch({ state: 'error', error: 'Network error' })
    patch({ state: 'active', loaded: 0, error: undefined })
    xhr.send(t.file)
  }

  const queue = (files: FileList | File[]) => {
    const list = [...files].map((file) => ({
      id: crypto.randomUUID(), name: file.name, dir: pathRef.current, file, loaded: 0, total: file.size, state: 'active' as const,
    }))
    setTransfers((ts) => [...ts, ...list])
    list.forEach((t) => upload(t))
  }

  const onDrop = (e: DragEvent) => {
    e.preventDefault()
    setDrag(false)
    if (e.dataTransfer.files.length) queue(e.dataTransfer.files)
  }

  const openFile = async (e: Entry) => {
    const p = join(path, e.name)
    if (e.size > MAX_EDIT_BYTES) return download(e.name)
    setEditPath(p)
    try {
      const res = await fetch(`${base}/file?path=${encodeURIComponent(p)}`, { credentials: 'same-origin' })
      if (!res.ok) throw new ApiError(res.status, 'HTTP', (await res.json().catch(() => ({}))).msg ?? res.statusText)
      const buf = await res.arrayBuffer()
      let text: string
      try {
        text = new TextDecoder('utf-8', { fatal: true }).decode(buf)
      } catch {
        setEditPath(null)
        return download(e.name) // binary: hand it to the browser instead
      }
      setEditor({ path: p, text, original: text })
    } catch (err) {
      setError(message(err))
    } finally {
      setEditPath(null)
    }
  }

  const cdHere = () => {
    const s = liveSessions.get(sessionId)
    if (s?.isReady()) {
      s.write(new TextEncoder().encode(`cd -- ${quote(path)}\r`))
      s.focus()
    }
  }

  const crumbs = path === '/' ? [''] : path.split('/')

  return (
    <aside
      className={`sftp${drag ? ' drag' : ''}`}
      aria-label="Files"
      onDragOver={(e) => { e.preventDefault(); setDrag(true) }}
      onDragLeave={(e) => { if (e.currentTarget === e.target) setDrag(false) }}
      onDrop={onDrop}
    >
      <div className="sftp-bar">
        <button className="icon-btn sm" title="Up" aria-label="Parent folder" onClick={() => load(parent(path))} disabled={path === '/'}><Icon name="up" size={14} /></button>
        <form className="sftp-path" onSubmit={(e) => { e.preventDefault(); load(pathInput) }}>
          <input value={pathInput} onChange={(e) => setPathInput(e.target.value)} aria-label="Path" spellCheck={false} />
        </form>
        <button className="icon-btn sm" title="Refresh" aria-label="Refresh" onClick={() => load(path)}><Icon name="refresh" size={14} /></button>
        <button className="icon-btn sm" title="Close files" aria-label="Close files" onClick={onClose}><Icon name="x" size={14} /></button>
      </div>
      <div className="crumbs">
        {crumbs.map((c, i) => (
          <button key={i} onClick={() => load(crumbs.slice(0, i + 1).join('/') || '/')}>{c || '/'}</button>
        ))}
      </div>
      <div className="sftp-tools">
        <button className="btn" onClick={() => fileInput.current?.click()}><Icon name="upload" size={13} /> Upload</button>
        <button className="btn" onClick={() => setNewFolder('')}><Icon name="folderPlus" size={13} /> Folder</button>
        <button className="btn" onClick={cdHere} title="cd into this folder in the terminal"><Icon name="terminal" size={13} /> cd here</button>
        <input ref={fileInput} type="file" multiple hidden onChange={(e) => { if (e.target.files?.length) queue(e.target.files); e.target.value = '' }} />
      </div>

      {error && <div className="sftp-error" role="alert"><span>{error}</span><button onClick={() => setError('')} aria-label="Dismiss"><Icon name="x" size={12} /></button></div>}

      <div className="files" role="list">
        {newFolder !== null && (
          <form className="file editing" onSubmit={(e) => { e.preventDefault(); if (newFolder.trim()) op({ op: 'mkdir', path: join(path, newFolder.trim()) }); setNewFolder(null) }}>
            <Icon name="folder" size={15} />
            <input autoFocus value={newFolder} placeholder="New folder" onChange={(e) => setNewFolder(e.target.value)} onKeyDown={(e) => e.key === 'Escape' && setNewFolder(null)} onBlur={() => setNewFolder(null)} />
          </form>
        )}
        {loading && !entries.length && <div className="files-empty">Loading…</div>}
        {!loading && !entries.length && !error && <div className="files-empty">Empty folder. Drop files here to upload.</div>}
        {entries.map((e) => {
          const editing = rowEdit?.name === e.name ? rowEdit : null
          if (editing && editing.kind !== 'delete') {
            return (
              <form key={e.name} className="file editing" onSubmit={(ev) => {
                ev.preventDefault()
                const v = editing.value.trim()
                if (editing.kind === 'rename' && v && v !== e.name) op({ op: 'rename', path: join(path, e.name), to: join(path, v) })
                else if (editing.kind === 'chmod' && v !== e.perm) op({ op: 'chmod', path: join(path, e.name), mode: v })
                setRowEdit(null)
              }}>
                <Icon name={e.isDir ? 'folder' : 'file'} size={15} />
                <input autoFocus value={editing.value} aria-label={editing.kind === 'rename' ? 'New name' : 'Octal mode'}
                  className={editing.kind === 'chmod' ? 'mono' : ''}
                  onChange={(ev) => setRowEdit({ ...editing, value: ev.target.value })}
                  onKeyDown={(ev) => ev.key === 'Escape' && setRowEdit(null)} onBlur={() => setRowEdit(null)} />
              </form>
            )
          }
          return (
            <div key={e.name} role="listitem" className={`file${editPath === join(path, e.name) ? ' busy' : ''}`}
              onDoubleClick={() => (e.isDir ? load(join(path, e.name)) : openFile(e))}
              title={`${e.mode}  ${e.perm}`}>
              <span className={`ficon${e.isDir ? ' dir' : ''}`}><Icon name={e.isDir ? 'folder' : 'file'} size={15} /></span>
              <span className="nm">{e.name}{e.isLink && <em className="hint"> →</em>}</span>
              {editing?.kind === 'delete' ? (
                <span className="row-confirm" onDoubleClick={(ev) => ev.stopPropagation()}>
                  <button className="danger-txt" onClick={() => { op({ op: 'remove', path: join(path, e.name) }); setRowEdit(null) }}>{e.isDir ? 'Delete folder' : 'Delete'}</button>
                  <button onClick={() => setRowEdit(null)}>Keep</button>
                </span>
              ) : (
                <>
                  <span className="sz">{e.isDir ? '' : fmtSize(e.size)}</span>
                  <span className="mt">{fmtTime(e.mtime)}</span>
                  <span className="row-actions" onDoubleClick={(ev) => ev.stopPropagation()}>
                    {!e.isDir && <button title="Download" aria-label={`Download ${e.name}`} onClick={() => download(e.name)}><Icon name="download" size={13} /></button>}
                    {!e.isDir && <button title="Edit" aria-label={`Edit ${e.name}`} onClick={() => openFile(e)}><Icon name="edit" size={13} /></button>}
                    <button title="Rename" aria-label={`Rename ${e.name}`} onClick={() => setRowEdit({ name: e.name, kind: 'rename', value: e.name })}><Icon name="cursor" size={13} /></button>
                    <button title={`Permissions (${e.perm})`} aria-label={`Permissions of ${e.name}`} onClick={() => setRowEdit({ name: e.name, kind: 'chmod', value: e.perm })}><Icon name="lock" size={13} /></button>
                    <button title="Delete" aria-label={`Delete ${e.name}`} onClick={() => setRowEdit({ name: e.name, kind: 'delete', value: '' })}><Icon name="trash" size={13} /></button>
                  </span>
                </>
              )}
            </div>
          )
        })}
      </div>

      {transfers.length > 0 && (
        <div className="transfers">
          {transfers.map((t) => (
            <div key={t.id} className={`xfer ${t.state}`}>
              <div className="xfer-top">
                <span className="nm">{t.name}</span>
                <span className="xfer-state">
                  {t.state === 'active' && `${Math.round((t.loaded / Math.max(1, t.total)) * 100)}%`}
                  {t.state === 'done' && 'Uploaded'}
                  {t.state === 'error' && (t.error || 'Failed')}
                  {t.state === 'exists' && (
                    <>Exists · <button className="link" onClick={() => upload(t, true)}>Replace</button></>
                  )}
                </span>
                {t.state !== 'active' && t.state !== 'done' && (
                  <button className="xfer-x" aria-label="Dismiss" onClick={() => setTransfers((ts) => ts.filter((x) => x.id !== t.id))}><Icon name="x" size={11} /></button>
                )}
              </div>
              <div className="bar"><span style={{ width: `${(t.loaded / Math.max(1, t.total)) * 100}%` }} /></div>
            </div>
          ))}
        </div>
      )}
      {drag && <div className="drop-hint">Drop to upload to <b>{path}</b></div>}

      {editor && (
        <FileEditor
          path={editor.path}
          text={editor.text}
          dirty={editor.text !== editor.original}
          onChange={(text) => setEditor({ ...editor, text })}
          onSave={async () => {
            const res = await fetch(`${base}/file?overwrite=1&path=${encodeURIComponent(editor.path)}`, {
              method: 'PUT', credentials: 'same-origin', body: editor.text,
            })
            if (!res.ok) throw new Error((await res.json().catch(() => ({}))).msg ?? res.statusText)
            setEditor({ ...editor, original: editor.text })
            load(pathRef.current)
          }}
          onClose={() => setEditor(null)}
        />
      )}
    </aside>
  )
}

function FileEditor({ path, text, dirty, onChange, onSave, onClose }: {
  path: string; text: string; dirty: boolean; onChange: (t: string) => void; onSave: () => Promise<void>; onClose: () => void
}) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [confirmClose, setConfirmClose] = useState(false)
  const save = async () => {
    setBusy(true)
    setError('')
    try {
      await onSave()
    } catch (e) {
      setError(message(e))
    } finally {
      setBusy(false)
    }
  }
  const close = () => (dirty ? setConfirmClose(true) : onClose())
  return (
    <div className="scrim modal-scrim" onMouseDown={(e) => e.target === e.currentTarget && close()}>
      <div className="modal editor" role="dialog" aria-label={`Edit ${path}`}
        onKeyDown={(e) => {
          if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 's') { e.preventDefault(); save() }
          if (e.key === 'Escape') { e.preventDefault(); close() }
        }}>
        <div className="editor-bar">
          <span className="mono editor-path">{path}{dirty && <b className="dirty"> ●</b>}</span>
          {error && <span className="form-error" style={{ margin: 0 }}>{error}</span>}
          {confirmClose ? (
            <>
              <span className="pop-sub">Discard changes?</span>
              <button className="btn" onClick={onClose}>Discard</button>
              <button className="btn" onClick={() => setConfirmClose(false)}>Keep editing</button>
            </>
          ) : (
            <>
              <button className="btn" onClick={close}>Close</button>
              <button className="btn primary" onClick={save} disabled={busy || !dirty}>{busy ? 'Saving…' : 'Save'} <kbd>Ctrl S</kbd></button>
            </>
          )}
        </div>
        <textarea className="editor-text mono" value={text} onChange={(e) => onChange(e.target.value)} spellCheck={false} autoFocus
          onKeyDown={(e) => {
            if (e.key === 'Tab') {
              e.preventDefault()
              const t = e.currentTarget
              const { selectionStart: a, selectionEnd: b } = t
              onChange(text.slice(0, a) + '\t' + text.slice(b))
              requestAnimationFrame(() => t.setSelectionRange(a + 1, a + 1))
            }
          }} />
      </div>
    </div>
  )
}

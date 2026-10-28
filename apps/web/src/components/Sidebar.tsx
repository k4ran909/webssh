import { useState } from 'react'
import { hasCredentials, type SavedHost } from '../lib/vault'
import type { Session } from '../lib/workspace'
import { Icon } from './icons'

interface Props {
  hosts: SavedHost[]
  sessions: Session[]
  syncError?: string
  onOpen: (h: SavedHost) => void
  onAdd: () => void
  onEdit: (h: SavedHost) => void
  onDelete: (id: string) => void
}

export function Sidebar({ hosts, sessions, syncError, onOpen, onAdd, onEdit, onDelete }: Props) {
  const [q, setQ] = useState('')
  const [confirm, setConfirm] = useState<string | null>(null)
  const needle = q.trim().toLowerCase()
  const shown = hosts.filter((h) => !needle || `${h.label} ${h.user}@${h.host}`.toLowerCase().includes(needle))
  const openCount = (h: SavedHost) =>
    sessions.filter((s) => s.target.host === h.host && s.target.port === h.port && s.target.user === h.user).length

  return (
    <aside className="sidebar">
      <div className="side-search">
        <Icon name="search" size={14} />
        <input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Filter hosts" aria-label="Filter hosts" />
      </div>
      <div className="side-head">
        <span>Hosts</span>
        <button className="icon-btn sm" title="Add host" aria-label="Add host" onClick={onAdd}><Icon name="plus" size={14} /></button>
      </div>
      <div className="host-list">
        {shown.map((h) => {
          const n = openCount(h)
          return (
            <div key={h.id} className="host-row" role="button" tabIndex={0} onClick={() => onOpen(h)} onKeyDown={(e) => e.key === 'Enter' && onOpen(h)}>
              <span className="swatch tall" style={{ background: h.color }} />
              <span className="host-text">
                <span className="host-label">{h.label || h.host}</span>
                <span className="host-addr">{h.user}@{h.host}{h.port === 22 ? '' : ':' + h.port}</span>
              </span>
              {hasCredentials(h) && <span className="cred-badge" title="Credentials saved in vault"><Icon name="lock" size={12} /></span>}
              {n > 0 && <span className="open-count" title={`${n} open`}>{n}</span>}
              {confirm === h.id ? (
                <span className="row-actions show" onClick={(e) => e.stopPropagation()}>
                  <button className="danger-txt" onClick={() => { onDelete(h.id); setConfirm(null) }}>Delete</button>
                  <button onClick={() => setConfirm(null)}>Keep</button>
                </span>
              ) : (
                <span className="row-actions" onClick={(e) => e.stopPropagation()}>
                  <button title="Edit" aria-label={`Edit ${h.label || h.host}`} onClick={() => onEdit(h)}><Icon name="edit" size={13} /></button>
                  <button title="Delete" aria-label={`Delete ${h.label || h.host}`} onClick={() => setConfirm(h.id)}><Icon name="trash" size={13} /></button>
                </span>
              )}
            </div>
          )
        })}
        {hosts.length === 0 && (
          <div className="side-empty">
            <p>No saved hosts yet.</p>
            <button className="link" onClick={onAdd}>Add your first host</button>
          </div>
        )}
        {hosts.length > 0 && shown.length === 0 && <div className="side-empty"><p>No match.</p></div>}
      </div>
      <div className={`side-foot${syncError ? ' bad' : ''}`} title={syncError}>
        <Icon name={syncError ? 'alert' : 'lock'} size={12} /> {syncError ? 'Vault sync failed, retrying' : 'End-to-end encrypted vault'}
      </div>
    </aside>
  )
}

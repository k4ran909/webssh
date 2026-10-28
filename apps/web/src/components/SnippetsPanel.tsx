import { useState } from 'react'
import type { Snippet } from '../lib/storage'
import { Icon } from './icons'

interface Props {
  snippets: Snippet[]
  canRun: boolean
  broadcast: boolean
  onRun: (s: Snippet) => void
  onChange: (s: Snippet[]) => void
}

export function SnippetsPanel({ snippets, canRun, broadcast, onRun, onChange }: Props) {
  const [name, setName] = useState('')
  const [command, setCommand] = useState('')

  const add = () => {
    if (!name.trim() || !command.trim()) return
    onChange([...snippets, { id: crypto.randomUUID(), name: name.trim(), command }])
    setName('')
    setCommand('')
  }

  return (
    <div className="snippets">
      <div className="pop-head">
        <span>Snippets</span>
        <span className="pop-sub">{!canRun ? 'Connect a session to run' : broadcast ? 'Runs in all sessions' : 'Runs in focused session'}</span>
      </div>
      <div className="snip-list">
        {snippets.map((s) => (
          <div key={s.id} className="snip">
            <button className="snip-run" disabled={!canRun} onClick={() => onRun(s)} title={`Run: ${s.command}`}>
              <Icon name="play" size={12} />
              <span className="snip-text">
                <span className="snip-name">{s.name}</span>
                <span className="snip-cmd">{s.command}</span>
              </span>
            </button>
            <button className="snip-del" title="Delete snippet" aria-label={`Delete ${s.name}`} onClick={() => onChange(snippets.filter((x) => x.id !== s.id))}>
              <Icon name="trash" size={13} />
            </button>
          </div>
        ))}
        {snippets.length === 0 && <p className="pop-sub" style={{ padding: '8px 10px' }}>No snippets.</p>}
      </div>
      <form className="snip-add" onSubmit={(e) => { e.preventDefault(); add() }}>
        <input className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="Name" aria-label="Snippet name" />
        <input className="input mono" value={command} onChange={(e) => setCommand(e.target.value)} placeholder="Command" aria-label="Snippet command" spellCheck={false} />
        <button className="btn" type="submit" disabled={!name.trim() || !command.trim()}><Icon name="plus" size={14} /> Add</button>
      </form>
    </div>
  )
}

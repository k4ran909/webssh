import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'

export interface PaletteItem {
  id: string
  section: string
  label: string
  hint?: string
  icon: ReactNode
  run: () => void
}

export function CommandPalette({ items, onClose }: { items: PaletteItem[]; onClose: () => void }) {
  const [q, setQ] = useState('')
  const [sel, setSel] = useState(0)
  const listRef = useRef<HTMLDivElement>(null)

  const shown = useMemo(() => {
    const words = q.toLowerCase().split(/\s+/).filter(Boolean)
    return items.filter((it) => {
      const hay = `${it.section} ${it.label} ${it.hint ?? ''}`.toLowerCase()
      return words.every((w) => hay.includes(w))
    })
  }, [items, q])

  useEffect(() => setSel(0), [q])
  useEffect(() => {
    listRef.current?.querySelector('.sel')?.scrollIntoView({ block: 'nearest' })
  }, [sel])

  const run = (it: PaletteItem | undefined) => {
    if (!it) return
    onClose()
    it.run()
  }

  let section = ''
  return (
    <div className="scrim palette-scrim" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className="palette" role="dialog" aria-label="Command palette">
        <div className="pal-in">
          <svg className="i" viewBox="0 0 24 24" aria-hidden><path d="M11 4.5a6.5 6.5 0 1 0 0 13a6.5 6.5 0 1 0 0-13M20 20l-4.2-4.2" /></svg>
          <input
            autoFocus
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder="Connect, switch session, run a snippet…"
            aria-label="Command"
            onKeyDown={(e) => {
              if (e.key === 'ArrowDown') { setSel((s) => Math.min(s + 1, shown.length - 1)); e.preventDefault() }
              if (e.key === 'ArrowUp') { setSel((s) => Math.max(s - 1, 0)); e.preventDefault() }
              // preventDefault stops the trailing keypress from landing in the terminal we focus next
              if (e.key === 'Enter') { e.preventDefault(); run(shown[sel]) }
              if (e.key === 'Escape') onClose()
            }}
          />
          <kbd>Esc</kbd>
        </div>
        <div className="pal-list" ref={listRef}>
          {shown.length === 0 && <div className="pal-empty">No matches</div>}
          {shown.map((it, i) => {
            const head = it.section !== section ? <div className="pal-sec">{(section = it.section)}</div> : null
            return (
              <div key={it.id}>
                {head}
                <button className={`pal-item${i === sel ? ' sel' : ''}`} onMouseMove={() => setSel(i)} onClick={() => run(it)}>
                  <span className="pal-icon">{it.icon}</span>
                  <span className="pal-label">{it.label}</span>
                  {it.hint && <span className="pal-hint">{it.hint}</span>}
                </button>
              </div>
            )
          })}
        </div>
        <div className="pal-foot"><span>↑↓ navigate</span><span>↵ run</span><span>Esc close</span></div>
      </div>
    </div>
  )
}

import { useState } from 'react'
import { explain, MAX_EXPLAIN, useAI } from '../lib/ai'
import { Icon } from './icons'

/** Renders the answer: fenced code blocks as code, everything else as paragraphs. */
function Answer({ text }: { text: string }) {
  const parts = text.split(/```[a-zA-Z0-9_-]*\n?/)
  return (
    <div className="ai-answer">
      {parts.map((p, i) =>
        i % 2 === 1 ? (
          <pre key={i} className="secret mono">{p.replace(/\n$/, '')}</pre>
        ) : (
          p.split(/\n{2,}/).filter((s) => s.trim()).map((para, j) => <p key={`${i}-${j}`}>{para.trim()}</p>)
        ),
      )}
    </div>
  )
}

/**
 * Shows exactly what will be sent before anything leaves the browser, then
 * the explanation.
 */
export function ExplainDialog({ text, where, onClose }: { text: string; where: string; onClose: () => void }) {
  const { model } = useAI()
  const [busy, setBusy] = useState(false)
  const [answer, setAnswer] = useState('')
  const [error, setError] = useState('')
  const sel = text.trim()
  const tooLong = new TextEncoder().encode(sel).length > MAX_EXPLAIN

  const run = async () => {
    setBusy(true)
    setError('')
    try {
      setAnswer((await explain(sel, where)).answer)
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Something went wrong.')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="scrim modal-scrim" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className="modal wide-modal" role="dialog" aria-label="Explain output" onKeyDown={(e) => e.key === 'Escape' && onClose()}>
        <div className="acct-head">
          <div>
            <h2>Explain output</h2>
            <p className="pop-sub">From {where}</p>
          </div>
          <button className="icon-btn" aria-label="Close" onClick={onClose}><Icon name="x" /></button>
        </div>
        <div className="acct-body">
          {!sel ? (
            <p className="pop-sub">Select some output in the terminal first (drag over it), then try again.</p>
          ) : answer ? (
            <Answer text={answer} />
          ) : (
            <div className="stack-form">
              <p className="warn-note"><Icon name="alert" size={13} /> This sends the text below, and only that, to {model ?? 'Claude'} (Anthropic) through your gateway. Check it holds no passwords, keys or other secrets.</p>
              <pre className="secret mono ai-preview">{sel}</pre>
              {tooLong && <p className="form-error">That's more than 16 KB. Select less output.</p>}
              {error && <p className="form-error">{error}</p>}
              <div className="form-actions">
                <button className="btn lg" onClick={onClose}>Cancel</button>
                <button className="btn primary lg" onClick={run} disabled={busy || tooLong} autoFocus>{busy ? 'Thinking…' : 'Explain'}</button>
              </div>
            </div>
          )}
        </div>
      </div>
    </div>
  )
}

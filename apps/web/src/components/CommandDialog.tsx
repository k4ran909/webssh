import { useState, type FormEvent } from 'react'
import { api } from '../lib/api'
import { liveSessions } from '../lib/live'
import { useAI } from '../lib/ai'
import { Icon } from './icons'

/**
 * "Ask AI for a command": the answer is shown for review and only ever typed
 * into the terminal, never run; the user presses Enter themselves.
 */
export function CommandDialog({ sessionId, where, onClose }: { sessionId: string | null; where: string; onClose: () => void }) {
  const { model } = useAI()
  const [request, setRequest] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [result, setResult] = useState<{ command: string; explanation: string } | null>(null)
  const [copied, setCopied] = useState(false)
  const live = sessionId ? liveSessions.get(sessionId) : undefined
  const oneLine = !!result && !result.command.includes('\n')

  const ask = async (e: FormEvent) => {
    e.preventDefault()
    if (!request.trim()) return
    setBusy(true)
    setError('')
    try {
      setResult(await api<{ command: string; explanation: string }>('POST', '/api/ai/command', { request: request.trim(), where }))
      setCopied(false)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Something went wrong.')
    } finally {
      setBusy(false)
    }
  }

  const typeIt = () => {
    if (!result || !live?.isReady()) return
    live.write(new TextEncoder().encode(result.command))
    onClose()
    live.focus()
  }

  return (
    <div className="scrim modal-scrim" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className="modal wide-modal" role="dialog" aria-label="Ask for a command" onKeyDown={(e) => e.key === 'Escape' && onClose()}>
        <div className="acct-head">
          <div>
            <h2>Ask for a command</h2>
            <p className="pop-sub">Describe what you want to do. Only your description goes to {model ?? 'Claude'} (Anthropic), through your gateway.</p>
          </div>
          <button className="icon-btn" aria-label="Close" onClick={onClose}><Icon name="x" /></button>
        </div>
        <form className="stack-form" onSubmit={ask}>
          <input className="input" value={request} onChange={(e) => setRequest(e.target.value)} placeholder="e.g. find the 10 biggest files under /var" autoFocus maxLength={2000} />
          <div className="acts"><button className="btn primary" type="submit" disabled={busy || !request.trim()}>{busy ? 'Thinking…' : result ? 'Ask again' : 'Ask'}</button></div>
        </form>
        {error && <p className="form-error">{error}</p>}
        {result && (
          <div className="stack-form" style={{ marginTop: 14 }}>
            <textarea className="textarea mono ai-command" value={result.command} spellCheck={false}
              onChange={(e) => setResult({ ...result, command: e.target.value })} aria-label="Suggested command" />
            {result.explanation && <p className="pop-sub">{result.explanation}</p>}
            <p className="warn-note"><Icon name="alert" size={13} /> Read it before you run it. AI can be wrong.</p>
            <div className="form-actions">
              <button className="btn lg" onClick={() => navigator.clipboard?.writeText(result.command).then(() => setCopied(true)).catch(() => {})}>
                <Icon name="copy" size={13} /> {copied ? 'Copied' : 'Copy'}
              </button>
              {oneLine && live && (
                <button className="btn primary lg" onClick={typeIt} disabled={!live.isReady()} title="Types it at the prompt; press Enter yourself to run it">
                  Type into terminal
                </button>
              )}
            </div>
            {!oneLine && <p className="pop-sub">It's more than one line, so copy it rather than typing it in (each line would run as you go).</p>}
          </div>
        )}
      </div>
    </div>
  )
}

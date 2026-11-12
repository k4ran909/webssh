import { useCallback, useEffect, useState, type FormEvent } from 'react'
import {
  addMember, confirmMember, createTeam, deleteTeam, fingerprint, members as loadMembers, refreshTeams, removeMember, rotateTeamKey,
  revokeInvite, setRole, setTeamRequireTotp, useTeams, type Member, type PendingInvite, type Team,
} from '../lib/teams'
import type { Role } from '../lib/vault'
import { Icon } from './icons'

const message = (e: unknown) => (e instanceof Error ? e.message : 'Something went wrong.')
const ROLE_HELP: Record<Role, string> = {
  owner: 'Everything, including deleting the team',
  admin: 'Invite, confirm and remove members; edit hosts',
  member: 'Use and edit shared hosts',
  viewer: 'Use shared hosts (including their credentials), no edits',
}
const rank: Record<Role, number> = { viewer: 0, member: 1, admin: 2, owner: 3 }

export function TeamsDialog({ myUserId, onClose }: { myUserId: string; onClose: () => void }) {
  const { teams, myFingerprint } = useTeams()
  const [sel, setSel] = useState<string | null>(teams[0]?.id ?? null)
  const [creating, setCreating] = useState(teams.length === 0)
  const team = teams.find((t) => t.id === sel)

  useEffect(() => { refreshTeams().catch(() => {}) }, [])
  useEffect(() => {
    if (!sel && teams[0]) setSel(teams[0].id)
  }, [teams, sel])

  return (
    <div className="scrim modal-scrim" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className="modal teams-modal" role="dialog" aria-label="Teams" onKeyDown={(e) => e.key === 'Escape' && onClose()}>
        <div className="acct-head">
          <div>
            <h2>Teams</h2>
            <p className="pop-sub">Share hosts and keys with people you trust. Team vaults are end-to-end encrypted like yours.</p>
          </div>
          <button className="icon-btn" aria-label="Close" onClick={onClose}><Icon name="x" /></button>
        </div>
        <div className="teams-body">
          <nav className="team-list" aria-label="Your teams">
            {teams.map((t) => (
              <button key={t.id} className={`team-item${t.id === sel && !creating ? ' on' : ''}`} onClick={() => { setSel(t.id); setCreating(false) }}>
                <span className="team-name">{t.name}</span>
                <span className="team-meta">{t.role}{!t.confirmed ? ' · pending' : ''}</span>
              </button>
            ))}
            <button className={`team-item new${creating ? ' on' : ''}`} onClick={() => setCreating(true)}><Icon name="plus" size={13} /> New team</button>
            <div className="my-fp" title="Teammates check this when they confirm you">
              <span className="pop-sub">Your key fingerprint</span>
              <code className="mono">{myFingerprint || '…'}</code>
            </div>
          </nav>
          <div className="team-detail">
            {creating ? (
              <NewTeam onCreated={(id) => { setSel(id); setCreating(false) }} />
            ) : team ? (
              <TeamDetail key={team.id} team={team} myUserId={myUserId} onGone={() => setSel(null)} />
            ) : (
              <p className="pop-sub">Pick a team.</p>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}

function NewTeam({ onCreated }: { onCreated: (id: string) => void }) {
  const [name, setName] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!name.trim()) return
    setBusy(true)
    setError('')
    try {
      onCreated(await createTeam(name.trim()))
    } catch (err) {
      setError(message(err))
      setBusy(false)
    }
  }
  return (
    <form className="stack-form" onSubmit={submit}>
      <h3>New team</h3>
      <p className="pop-sub">You'll be its owner. A new encryption key is made in this browser; the server only ever sees it encrypted for each member.</p>
      <input className="input" placeholder="Team name, e.g. Platform" aria-label="Team name" value={name} onChange={(e) => setName(e.target.value)} autoFocus maxLength={80} />
      {error && <p className="form-error">{error}</p>}
      <div className="acts"><button className="btn primary" type="submit" disabled={busy || !name.trim()}>{busy ? 'Creating…' : 'Create team'}</button></div>
    </form>
  )
}

function TeamDetail({ team, myUserId, onGone }: { team: Team; myUserId: string; onGone: () => void }) {
  const [list, setList] = useState<Member[]>([])
  const [invites, setInvites] = useState<PendingInvite[]>([])
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [confirming, setConfirming] = useState<Member | null>(null)
  const [danger, setDanger] = useState<'leave' | 'delete' | null>(null)
  const isAdmin = rank[team.role] >= rank.admin

  const load = useCallback(async () => {
    try {
      const r = await loadMembers(team.id)
      setList(r.members)
      setInvites(r.invites ?? [])
    } catch (e) {
      setError(message(e))
    }
  }, [team.id])
  useEffect(() => { load() }, [load])

  const act = async (f: () => Promise<unknown>, done?: string) => {
    setError('')
    setNotice('')
    try {
      await f()
      if (done) setNotice(done)
      await load()
    } catch (e) {
      setError(message(e))
    }
  }

  const canManage = (m: Member) => m.userId !== myUserId && isAdmin && (team.role === 'owner' ? m.role !== 'owner' : rank[m.role] < rank.admin)
  const assignable: Role[] = team.role === 'owner' ? ['admin', 'member', 'viewer'] : ['member', 'viewer']

  return (
    <div className="team-detail-inner">
      <div className="team-title">
        <h3>{team.name}</h3>
        <span className="tag">{team.role}</span>
      </div>
      {!team.confirmed && (
        <p className="warn-note"><Icon name="alert" size={14} /> A team admin still has to confirm you. Read them your key fingerprint (left) so they can check it's really you.</p>
      )}

      <section className="acct-sec">
        <h3>Members</h3>
        <div className="member-list">
          {list.map((m) => (
            <div key={m.userId} className="member-row">
              <div className="kc-main">
                <span className="kc-title">{m.email}{m.userId === myUserId && <em className="hint"> (you)</em>}</span>
                <span className="kc-sub">
                  {m.confirmed ? 'Confirmed' : m.publicKey ? 'Waiting for confirmation' : "Hasn't unlocked webssh yet"}
                  {!m.totp && team.requireTotp && ' · 2FA off'}
                </span>
              </div>
              {canManage(m) ? (
                <select className="input select" value={m.role} aria-label={`Role of ${m.email}`} title={ROLE_HELP[m.role]}
                  onChange={(e) => act(() => setRole(team.id, m.userId, e.target.value as Role))}>
                  {[...new Set([m.role, ...assignable])].map((r) => <option key={r} value={r}>{r}</option>)}
                </select>
              ) : (
                <span className="tag" title={ROLE_HELP[m.role]}>{m.role}</span>
              )}
              {isAdmin && !m.confirmed && m.publicKey && team.confirmed && (
                <button className="btn primary sm-btn" onClick={() => setConfirming(m)}>Confirm</button>
              )}
              {canManage(m) && (
                <button className="icon-btn sm" title="Remove from team" aria-label={`Remove ${m.email}`}
                  onClick={() => act(() => removeMember(team.id, m.userId, m.confirmed), m.confirmed
                    ? `${m.email} was removed and the team key was replaced. Change any passwords or keys they could see.`
                    : `${m.email} was removed.`)}>
                  <Icon name="x" size={13} />
                </button>
              )}
            </div>
          ))}
          {invites.map((i) => (
            <div key={i.email} className="member-row invited">
              <div className="kc-main">
                <span className="kc-title">{i.email}</span>
                <span className="kc-sub">Invited · expires {new Date(i.expiresAt).toLocaleDateString()}</span>
              </div>
              <span className="tag">{i.role}</span>
              <button className="icon-btn sm" title="Revoke invitation" aria-label={`Revoke ${i.email}`} onClick={() => act(() => revokeInvite(team.id, i.email))}><Icon name="x" size={13} /></button>
            </div>
          ))}
        </div>
        {isAdmin && <InviteForm teamId={team.id} roles={assignable} onDone={load} />}
      </section>

      {isAdmin && (
        <section className="acct-sec">
          <h3>Policy</h3>
          <label className="check">
            <input type="checkbox" checked={team.requireTotp} onChange={(e) => act(() => setTeamRequireTotp(team.id, e.target.checked))} />
            <span>Require two-factor sign-in to open this team's vault</span>
          </label>
          <div className="acts">
            <button className="btn" onClick={() => act(() => rotateTeamKey(team.id), 'Team key replaced. Everyone still in the team keeps access.')}>
              <Icon name="key" size={13} /> Replace team key
            </button>
          </div>
          <p className="pop-sub">Re-encrypts the team vault with a new key, for current members only. Happens automatically when you remove someone.</p>
        </section>
      )}

      <section className="acct-sec">
        <h3>{team.role === 'owner' ? 'Delete team' : 'Leave team'}</h3>
        <p className="pop-sub">
          {team.role === 'owner'
            ? 'Deletes the team vault for everyone. Hosts members copied to their own vaults stay there.'
            : "You'll lose access to its hosts and keys. Anything you already copied stays yours."}
        </p>
        {danger ? (
          <div className="acts">
            <button className="btn danger" onClick={() => act(async () => {
              if (danger === 'delete') await deleteTeam(team.id)
              else await removeMember(team.id, myUserId)
              onGone()
            })}>{danger === 'delete' ? `Delete ${team.name}` : `Leave ${team.name}`}</button>
            <button className="btn" onClick={() => setDanger(null)}>Cancel</button>
          </div>
        ) : (
          <div className="acts"><button className="btn" onClick={() => setDanger(team.role === 'owner' ? 'delete' : 'leave')}>{team.role === 'owner' ? 'Delete team…' : 'Leave team…'}</button></div>
        )}
      </section>

      {error && <p className="form-error">{error}</p>}
      {notice && <p className="ok-note">{notice}</p>}

      {confirming && (
        <ConfirmMember teamId={team.id} member={confirming}
          onDone={() => { setConfirming(null); act(async () => {}, `${confirming.email} can now open the team vault.`) }}
          onCancel={() => setConfirming(null)} />
      )}
    </div>
  )
}

function InviteForm({ teamId, roles, onDone }: { teamId: string; roles: Role[]; onDone: () => void }) {
  const [email, setEmail] = useState('')
  const [role, setRoleState] = useState<Role>('member')
  const [link, setLink] = useState('')
  const [msg, setMsg] = useState('')
  const [error, setError] = useState('')
  const [copied, setCopied] = useState(false)
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setError('')
    setLink('')
    setMsg('')
    try {
      const r = await addMember(teamId, email.trim(), role)
      if (r.status === 'invited' && r.link) {
        setLink(location.origin + r.link)
        setMsg(`${email.trim()} has no account yet. Send them this link (valid 7 days); after they sign up, confirm them here.`)
      } else {
        setMsg(`${email.trim()} was added. Confirm them once you've checked their key fingerprint.`)
      }
      setEmail('')
      onDone()
    } catch (err) {
      setError(message(err))
    }
  }
  return (
    <form className="invite-form" onSubmit={submit}>
      <input className="input" type="email" placeholder="Email address" aria-label="Email to invite" value={email} onChange={(e) => setEmail(e.target.value)} />
      <select className="input select" value={role} aria-label="Role" onChange={(e) => setRoleState(e.target.value as Role)}>
        {roles.map((r) => <option key={r} value={r}>{r}</option>)}
      </select>
      <button className="btn" type="submit" disabled={!email.trim()}><Icon name="plus" size={13} /> Add</button>
      {msg && <p className="pop-sub full">{msg}</p>}
      {link && (
        <div className="invite-link full">
          <code className="secret">{link}</code>
          <button type="button" className="btn" onClick={() => navigator.clipboard?.writeText(link).then(() => setCopied(true)).catch(() => {})}>
            <Icon name="copy" size={13} /> {copied ? 'Copied' : 'Copy'}
          </button>
        </div>
      )}
      {error && <p className="form-error full">{error}</p>}
    </form>
  )
}

function ConfirmMember({ teamId, member, onDone, onCancel }: { teamId: string; member: Member; onDone: () => void; onCancel: () => void }) {
  const [fp, setFp] = useState('')
  const [checked, setChecked] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  useEffect(() => { if (member.publicKey) fingerprint(member.publicKey).then(setFp) }, [member.publicKey])
  return (
    <div className="scrim modal-scrim" onMouseDown={(e) => e.target === e.currentTarget && onCancel()}>
      <div className="modal" role="dialog" aria-label={`Confirm ${member.email}`}>
        <div className="acct-head"><h2>Confirm {member.email}</h2></div>
        <p className="pop-sub">Confirming encrypts the team key for this person. Ask them to open <b>Teams</b> and read you their key fingerprint. It must match exactly; if it doesn't, someone may be impersonating them.</p>
        <div className="rcode mono fp-check" aria-label="Their key fingerprint">{fp.split(' ').map((g, i) => <span key={i}>{g}</span>)}</div>
        <label className="check">
          <input type="checkbox" checked={checked} onChange={(e) => setChecked(e.target.checked)} />
          <span>It matches what they see</span>
        </label>
        {error && <p className="form-error">{error}</p>}
        <div className="form-actions">
          <button className="btn lg" onClick={onCancel}>Cancel</button>
          <button className="btn primary lg" disabled={!checked || busy} onClick={async () => {
            setBusy(true)
            try {
              await confirmMember(teamId, member)
              onDone()
            } catch (e) {
              setError(message(e))
              setBusy(false)
            }
          }}>{busy ? 'Confirming…' : 'Confirm member'}</button>
        </div>
      </div>
    </div>
  )
}

import { useEffect, useState } from 'react'
import { getThemePref, saveThemePref } from '../lib/storage'
import { Icon } from './icons'

const darkQuery = () => matchMedia('(prefers-color-scheme: dark)')

export function applyStoredTheme() {
  const pref = getThemePref()
  if (pref !== 'system') document.documentElement.dataset.theme = pref
}

function effectiveTheme(): 'light' | 'dark' {
  const set = document.documentElement.dataset.theme
  if (set === 'light' || set === 'dark') return set
  return darkQuery().matches ? 'dark' : 'light'
}

export function ThemeToggle() {
  const [theme, setTheme] = useState(effectiveTheme)

  useEffect(() => {
    const q = darkQuery()
    const onChange = () => setTheme(effectiveTheme())
    q.addEventListener('change', onChange)
    return () => q.removeEventListener('change', onChange)
  }, [])

  const toggle = () => {
    const next = theme === 'dark' ? 'light' : 'dark'
    document.documentElement.dataset.theme = next
    saveThemePref(next)
    setTheme(next)
  }

  return (
    <button className="icon-btn" onClick={toggle} title="Toggle theme" aria-label="Toggle theme">
      <Icon name={theme === 'dark' ? 'sun' : 'moon'} />
    </button>
  )
}

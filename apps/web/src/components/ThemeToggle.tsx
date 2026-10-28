import { useEffect, useState } from 'react'
import { getThemePref, saveThemePref, type ThemePref } from '../lib/storage'

const darkQuery = () => matchMedia('(prefers-color-scheme: dark)')

export function applyStoredTheme() {
  const pref = getThemePref()
  if (pref !== 'system') document.documentElement.dataset.theme = pref
}

/** Theme preference plus the resolved light/dark value. */
export function useTheme() {
  const [pref, setPrefState] = useState<ThemePref>(getThemePref)
  const [systemDark, setSystemDark] = useState(() => darkQuery().matches)

  useEffect(() => {
    const q = darkQuery()
    const onChange = () => setSystemDark(q.matches)
    q.addEventListener('change', onChange)
    return () => q.removeEventListener('change', onChange)
  }, [])

  const setPref = (p: ThemePref) => {
    if (p === 'system') delete document.documentElement.dataset.theme
    else document.documentElement.dataset.theme = p
    saveThemePref(p)
    setPrefState(p)
  }

  const resolved: 'light' | 'dark' = pref === 'system' ? (systemDark ? 'dark' : 'light') : pref
  return { pref, resolved, setPref }
}

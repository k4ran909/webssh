const PIECE = 'M156 0h16c0 92 32 128 128 128v16H156z'

export function Logo({ size = 20, apart = false }: { size?: number; apart?: boolean }) {
  return (
    <svg className={apart ? 'mark apart' : 'mark'} width={size} height={size} viewBox="0 0 300 300" fill="currentColor" aria-hidden>
      <g className="a"><path d={PIECE} /></g>
      <g className="b"><path d={PIECE} transform="rotate(180 150 150)" /></g>
    </svg>
  )
}

const PATHS = {
  sun: 'M12 8a4 4 0 1 0 0 8a4 4 0 1 0 0-8M12 2.5v2M12 19.5v2M2.5 12h2M19.5 12h2M5.3 5.3l1.4 1.4M17.3 17.3l1.4 1.4M5.3 18.7l1.4-1.4M17.3 6.7l1.4-1.4',
  moon: 'M20 14.5A8 8 0 0 1 9.5 4a8 8 0 1 0 10.5 10.5z',
  arrow: 'M5 12h14M13 6l6 6-6 6',
  shield: 'M12 3l7 3v6c0 4.5-3 7.5-7 9-4-1.5-7-4.5-7-9V6z',
  alert: 'M12 4 2.5 20h19zM12 10v4M12 17h.01',
}

export function Icon({ name, size = 16 }: { name: keyof typeof PATHS; size?: number }) {
  return (
    <svg className="i" viewBox="0 0 24 24" width={size} height={size} aria-hidden>
      <path d={PATHS[name]} />
    </svg>
  )
}

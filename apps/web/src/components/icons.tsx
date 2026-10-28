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
  x: 'M6 6l12 12M18 6 6 18',
  plus: 'M12 5v14M5 12h14',
  minus: 'M5 12h14',
  min: 'M6 17h12',
  max: 'M5.5 5.5h13v13h-13z',
  restore: 'M8.5 8.5h10v10h-10zM5.5 15.5v-10h10',
  up: 'm6 15 6-6 6 6',
  down: 'm6 9 6 6 6-6',
  search: 'M11 4.5a6.5 6.5 0 1 0 0 13a6.5 6.5 0 1 0 0-13M20 20l-4.2-4.2',
  copy: 'M9 9h10v10H9zM5 15V5h10',
  sidebar: 'M4.5 5.5h15v13h-15zM9.5 5.5v13',
  windows: 'M4.5 4.5h9v8h-9zM10.5 11.5h9v8h-9z',
  tabs: 'M3.5 8.5h17v11h-17zM3.5 8.5v-3h6l1.5 3',
  tile: 'M4.5 4.5h6.5v6.5H4.5zM13 4.5h6.5v6.5H13zM4.5 13h6.5v6.5H4.5zM13 13h6.5v6.5H13z',
  cascade: 'M4 4h11v8H4zM9 12v6h11v-8h-5',
  broadcast: 'M12 12h.01M8.5 8.5a5 5 0 0 0 0 7M15.5 8.5a5 5 0 0 1 0 7M5.6 5.6a9 9 0 0 0 0 12.8M18.4 5.6a9 9 0 0 1 0 12.8',
  bolt: 'M13 3 5 13.5h6L10 21l8-10.5h-6z',
  gear: 'M12 9a3 3 0 1 0 0 6a3 3 0 1 0 0-6M12 3v2.5M12 18.5V21M3 12h2.5M18.5 12H21M5.6 5.6l1.8 1.8M16.6 16.6l1.8 1.8M5.6 18.4l1.8-1.8M16.6 7.4l1.8-1.8',
  server: 'M4.5 4.5h15v6h-15zM4.5 13.5h15v6h-15zM8 7.5h.01M8 16.5h.01',
  edit: 'M4 20h4L19 9l-4-4L4 16zM13.5 6.5l4 4',
  trash: 'M5 7h14M9 7V4.5h6V7M7 7l1 13h8l1-13',
  play: 'M7 5v14l11-7z',
  terminal: 'M4.5 4.5h15v15h-15zM8 10l2.5 2L8 14M12.5 14.5H16',
  lock: 'M6 11h12v9H6zM8.5 11V8a3.5 3.5 0 0 1 7 0v3',
  user: 'M12 4a4 4 0 1 0 0 8a4 4 0 1 0 0-8M4.5 20a7.5 7.5 0 0 1 15 0',
  activity: 'M3 12h4l3-8 4 16 3-8h4',
  key: 'M8 11a4 4 0 1 0 0 8a4 4 0 1 0 0-8M11 13l9-9M16.5 7.5l2.5 2.5M14 10l2 2',
  logout: 'M15 4.5h3.5v15H15M10 8l-4 4 4 4M6 12h9',
  command:'M9 6a3 3 0 1 0-3 3h12a3 3 0 1 0-3-3v12a3 3 0 1 0 3-3H6a3 3 0 1 0 3 3z',
}

export type IconName = keyof typeof PATHS

export function Icon({ name, size = 16 }: { name: IconName; size?: number }) {
  return (
    <svg className="i" viewBox="0 0 24 24" width={size} height={size} aria-hidden>
      <path d={PATHS[name]} />
    </svg>
  )
}

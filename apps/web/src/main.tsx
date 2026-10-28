import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import '@fontsource/geist-sans/400.css'
import '@fontsource/geist-sans/500.css'
import '@fontsource/geist-sans/600.css'
import '@fontsource/geist-mono/400.css'
import '@fontsource/geist-mono/500.css'
import '@xterm/xterm/css/xterm.css'
import './styles/tokens.css'
import './styles/app.css'
import { Root } from './Root'
import { applyStoredTheme } from './components/ThemeToggle'

applyStoredTheme()

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <Root />
  </StrictMode>,
)

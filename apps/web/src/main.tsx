import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { App } from './App'
import appIconUrl from '../../../assets/brand/vc-workspace-app-icon.svg?url'
import './styles.css'

document.querySelector<HTMLLinkElement>('#vc-workspace-icon')?.setAttribute('href', appIconUrl)

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
)

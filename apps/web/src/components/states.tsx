import { RefreshCw } from 'lucide-react'

/** Replaces page-specific loading, error and empty blocks with one stable content-state geometry. */
export function LoadingState({ label }: { label: string }) {
  return <div className="state-panel" aria-live="polite"><RefreshCw aria-hidden="true" data-spinning="true" /><p>{label}</p></div>
}

export function ErrorState({ title, detail, action, onRetry }: { title: string; detail: string; action: string; onRetry: () => void }) {
  return <div className="state-panel" role="alert"><h2>{title}</h2><p>{detail}</p><button className="button" type="button" onClick={onRetry}>{action}</button></div>
}

export function EmptyState({ title, detail, action, onAction }: { title: string; detail?: string; action?: string; onAction?: () => void }) {
  return <div className="state-panel"><h2>{title}</h2>{detail && <p>{detail}</p>}{action && onAction && <button className="button" type="button" onClick={onAction}>{action}</button>}</div>
}

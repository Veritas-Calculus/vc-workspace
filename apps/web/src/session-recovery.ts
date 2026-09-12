let redirecting = false

// A full navigation discards private caches, pending dialogs and secrets. Never
// persist a password-bearing draft or retry a mutation under a new session.
export function expireWebSession() {
  if (redirecting || typeof window === 'undefined') return
  redirecting = true
  window.location.replace('/console?session=expired')
}

export function clearSessionExpiry() {
  redirecting = false
  if (typeof window !== 'undefined' && new URLSearchParams(window.location.search).get('session') === 'expired') {
    window.history.replaceState(null, '', '/console')
  }
}

export const VC_WORKSPACE_URL_SCHEME = 'vc-workspace'

export function desktopAppLink(vmid: number): string {
  if (!Number.isSafeInteger(vmid) || vmid <= 0) throw new RangeError('vmid must be a positive integer')
  return `${VC_WORKSPACE_URL_SCHEME}://connect?vmid=${vmid}`
}

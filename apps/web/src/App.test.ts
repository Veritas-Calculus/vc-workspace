import { describe, expect, it } from 'vitest'
import { eligibleNodesForGPU } from './App'
import type { Infrastructure, PlatformConfig } from './api'
import { filterManagedDesktops, managedDesktops, WORKSPACES } from './console/workspaces'
import { APP_ROUTES, isConsolePath, SOURCE_REPOSITORY } from './landing'
import { desktopAppLink } from './app-link'

const infrastructure = {
  nodes: [
    { name: 'infra-node3', status: 'online' },
    { name: 'infra-node4', status: 'online' },
    { name: 'infra-node5', status: 'offline' },
  ],
} as Infrastructure

const platform = {
  gpu_profiles: [
    { id: 'none', mode: 'none', enabled: true },
    {
      id: 'intel-igpu-passthrough', mode: 'pci_passthrough', enabled: true,
      vendor_id: '0x8086', device_class: '0x030000', resource_mapping: 'vc-vdi-intel-igpu', mdev_type: '',
    },
    {
      id: 'intel-gvtg-v5-4', mode: 'mdev', enabled: true,
      vendor_id: '0x8086', device_class: '0x030000', resource_mapping: 'vc-vdi-intel-gvtg', mdev_type: 'i915-GVTg_V5_4',
    },
  ],
  gpu_devices: [
    { node: 'infra-node3', id: '0000:00:02.0', vendor_id: '0x8086', class: '0x030000', assignable: true, mdev_types: [{ type: 'i915-GVTg_V5_4', name: 'GVTg_V5_4', available: 1, description: '' }] },
    { node: 'infra-node4', id: '0000:00:02.0', vendor_id: '0x8086', class: '0x030000', assignable: true, mdev_types: [{ type: 'i915-GVTg_V5_4', name: 'GVTg_V5_4', available: 0, description: '' }] },
  ],
  pci_resource_mappings: [
    { id: 'vc-vdi-intel-igpu', description: '', mdev: false, entries: [{ node: 'infra-node4', device_id: '0000:00:02.0', iommu_group: '7' }] },
    { id: 'vc-vdi-intel-gvtg', description: '', mdev: true, entries: [{ node: 'infra-node3', device_id: '0000:00:02.0' }, { node: 'infra-node4', device_id: '0000:00:02.0' }] },
  ],
} as PlatformConfig

const desktopInfrastructure = {
  virtual_machines: [
    { vmid: 158, name: 'vc-vdi-debian', node: 'infra-node6', status: 'running', template: false, managed: true },
    { vmid: 159, name: 'vc-vdi-windows', node: 'infra-node4', status: 'stopped', template: false, managed: true },
    { vmid: 9100, name: 'debian-template', node: 'infra-node3', status: 'stopped', template: true, managed: false },
    { vmid: 101, name: 'database', node: 'infra-node3', status: 'running', template: false, managed: false },
  ],
} as Infrastructure

describe('console workspaces', () => {
  it('maps the implemented product modules to seven stable workspaces', () => {
    expect(WORKSPACES.map((workspace) => workspace.id)).toEqual(['desktops', 'activity', 'infrastructure', 'images', 'gpu', 'access', 'audit'])
		expect(WORKSPACES.filter((workspace) => workspace.id !== 'desktops').every((workspace) => workspace.adminOnly)).toBe(true)
  })

  it('uses the server capability instead of a name prefix to select desktops', () => {
    expect(managedDesktops(desktopInfrastructure).map((machine) => machine.vmid)).toEqual([158, 159])
  })

  it('filters managed desktops by state and searchable infrastructure fields', () => {
    expect(filterManagedDesktops(desktopInfrastructure, 'node4', 'stopped', 'en').map((machine) => machine.vmid)).toEqual([159])
    expect(filterManagedDesktops(desktopInfrastructure, 'database', 'all', 'en')).toEqual([])
  })
})

describe('public routes', () => {
  it('keeps the public landing page separate from the console bootstrap', () => {
    expect(APP_ROUTES.landing).toBe('/')
    expect(isConsolePath('/')).toBe(false)
    expect(isConsolePath('/console')).toBe(true)
    expect(isConsolePath('/console/')).toBe(true)
  })

  it('links to the canonical open-source repository', () => {
    expect(SOURCE_REPOSITORY).toBe('https://github.com/Veritas-Calculus/vc-workspace')
  })
})

describe('native app links', () => {
  it('carries only the desktop identifier', () => {
    expect(desktopAppLink(158)).toBe('vc-vdi://connect?vmid=158')
    expect(desktopAppLink(158)).not.toMatch(/token|password|host/i)
  })

  it('rejects invalid desktop identifiers', () => {
    expect(() => desktopAppLink(0)).toThrow(RangeError)
    expect(() => desktopAppLink(1.5)).toThrow(RangeError)
  })
})

describe('eligibleNodesForGPU', () => {
  it('keeps every online node for the CPU-only profile', () => {
    expect(eligibleNodesForGPU(infrastructure, platform, 'none').map((node) => node.name)).toEqual(['infra-node3', 'infra-node4'])
  })

  it('pins passthrough placement to the mapped assignable node', () => {
    expect(eligibleNodesForGPU(infrastructure, platform, 'intel-igpu-passthrough').map((node) => node.name)).toEqual(['infra-node4'])
  })

  it('returns no target when the mapped device is not assignable', () => {
    const unavailable = { ...platform, gpu_devices: platform.gpu_devices.map((device) => ({ ...device, assignable: false })) }
    expect(eligibleNodesForGPU(infrastructure, unavailable, 'intel-igpu-passthrough')).toEqual([])
  })

  it('matches a PVE whole-device path to its display function', () => {
    const wholeDevice = {
      ...platform,
      pci_resource_mappings: [{
        id: 'vc-vdi-intel-igpu', description: '', mdev: false,
        entries: [{ node: 'infra-node4', device_id: '0000:00:02', device_paths: ['0000:00:02'], hardware_id: '8086:1912', iommu_group: '7' }],
      }],
    }
    expect(eligibleNodesForGPU(infrastructure, wholeDevice, 'intel-igpu-passthrough').map((node) => node.name)).toEqual(['infra-node4'])
  })

  it('uses live GVT-g capacity without requiring PCI passthrough readiness', () => {
    expect(eligibleNodesForGPU(infrastructure, platform, 'intel-gvtg-v5-4').map((node) => node.name)).toEqual(['infra-node3'])
  })
})

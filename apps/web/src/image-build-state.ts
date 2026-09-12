import type { ImageProfile, Job, PlatformConfig } from './api'

const profileStates: Record<Job['state'], ImageProfile['build_status']> = {
  accepted: 'building', running: 'building', succeeded: 'testing', failed: 'failed',
}

// Apply only a server-acknowledged job, never the form's proposed configuration.
// The full profile is refetched on completion; a succeeded build is not ready.
export function applyImageBuildJob(platform: PlatformConfig, job: Job): PlatformConfig {
  const profileID = job.request?.image_profile_id
  if (job.operation !== 'image.build' || !profileID || !platform.image_profiles.some((profile) => profile.id === profileID)) return platform
  const previous = platform.image_builds.find((item) => item.id === job.id)
  if (previous && Date.parse(previous.updated_at) > Date.parse(job.updated_at)) return platform
  return {
    ...platform,
    image_builds: [job, ...platform.image_builds.filter((item) => item.id !== job.id)],
    image_profiles: platform.image_profiles.map((profile) => profile.id !== profileID ? profile : {
      ...profile,
      build_status: profileStates[job.state],
      template_vmid: job.target_vmid ?? profile.template_vmid,
      source_node: job.target_node ?? profile.source_node,
      status_detail: job.error || job.detail || '',
    }),
  }
}

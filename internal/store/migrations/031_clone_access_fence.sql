-- A target remains unavailable until every platform clone attempt targeting it
-- has succeeded. Failed/uncertain attempts require explicit reconciliation;
-- inventory discovery and an assignment cannot release this fence.
CREATE INDEX pve_jobs_unsettled_clone_target ON pve_jobs(target_vmid)
 WHERE operation='pve.template_clone' AND state<>'succeeded';

CREATE FUNCTION desktop_clone_ready(vmid bigint) RETURNS boolean
LANGUAGE sql STABLE AS $$
 SELECT NOT EXISTS(SELECT 1 FROM pve_jobs j WHERE j.target_vmid=vmid
  AND j.operation='pve.template_clone' AND j.state<>'succeeded')
$$;

CREATE OR REPLACE VIEW effective_user_desktop_access AS
SELECT DISTINCT u.id AS user_id,d.vmid AS desktop_vmid
FROM users u JOIN managed_desktops d ON d.present AND d.enabled
WHERE NOT u.disabled AND desktop_clone_ready(d.vmid) AND (
  (d.access_mode='personal' AND d.owner_user_id=u.id) OR
  (d.access_mode='shared' AND (
    EXISTS(SELECT 1 FROM user_desktop_assignments a WHERE a.user_id=u.id AND a.desktop_vmid=d.vmid) OR
    EXISTS(SELECT 1 FROM group_desktop_assignments a
      JOIN identity_groups g ON g.id=a.group_id AND g.enabled
      JOIN identity_group_memberships m ON m.group_id=g.id
      WHERE m.user_id=u.id AND a.desktop_vmid=d.vmid)
  ))
);

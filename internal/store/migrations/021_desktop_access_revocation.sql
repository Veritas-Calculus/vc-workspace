-- Single effective-access definition for listing, authorization and revocation.
CREATE VIEW effective_user_desktop_access AS
SELECT DISTINCT u.id AS user_id,d.vmid AS desktop_vmid
FROM users u JOIN managed_desktops d ON d.present AND d.enabled
WHERE NOT u.disabled AND (
  (d.access_mode='personal' AND d.owner_user_id=u.id) OR
  (d.access_mode='shared' AND (
    EXISTS(SELECT 1 FROM user_desktop_assignments a WHERE a.user_id=u.id AND a.desktop_vmid=d.vmid) OR
    EXISTS(SELECT 1 FROM group_desktop_assignments a
      JOIN identity_groups g ON g.id=a.group_id AND g.enabled
      JOIN identity_group_memberships m ON m.group_id=g.id
      WHERE m.user_id=u.id AND a.desktop_vmid=d.vmid)
  ))
);

ALTER TABLE desktop_connection_sessions ADD COLUMN termination_required boolean NOT NULL DEFAULT false;
UPDATE desktop_connection_sessions s
SET state='revoking',termination_required=true,updated_at=now()
WHERE s.state IN ('active','revoking') AND NOT EXISTS (
  SELECT 1 FROM effective_user_desktop_access a WHERE a.user_id=s.user_id AND a.desktop_vmid=s.desktop_vmid
);

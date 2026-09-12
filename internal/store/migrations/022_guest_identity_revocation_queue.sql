ALTER TABLE guest_identity_bindings ADD COLUMN session_expires_at timestamptz;
UPDATE guest_identity_bindings b SET session_expires_at=c.expires_at
FROM (SELECT user_id,desktop_vmid,max(expires_at) AS expires_at FROM desktop_connection_sessions GROUP BY user_id,desktop_vmid) c
WHERE b.user_id=c.user_id AND b.desktop_vmid=c.desktop_vmid;

-- Independent of connection state: an ordinary disconnect preserves the OS
-- login, but removing authorization must still be able to revoke that login.
CREATE TABLE guest_identity_revocations (
  desktop_vmid bigint NOT NULL REFERENCES managed_desktops(vmid) ON DELETE CASCADE,
  user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  guest_username text NOT NULL,
  requested_revision bigint NOT NULL DEFAULT 1,
  completed_revision bigint NOT NULL DEFAULT 0,
  reason text NOT NULL CHECK(reason IN ('access_removed','credential_revoked','expired')),
  last_error text NOT NULL DEFAULT '',
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(desktop_vmid,user_id,guest_username),
  CHECK(completed_revision >= 0 AND requested_revision >= completed_revision)
);
CREATE INDEX guest_identity_revocations_pending_idx ON guest_identity_revocations(updated_at)
WHERE requested_revision > completed_revision;

WITH affected AS (
  UPDATE guest_identity_bindings b SET state='disabled',updated_at=now()
  WHERE NOT EXISTS(SELECT 1 FROM effective_user_desktop_access a WHERE a.user_id=b.user_id AND a.desktop_vmid=b.desktop_vmid)
     OR b.session_expires_at <= now()
  RETURNING b.desktop_vmid,b.user_id,b.guest_username,b.session_expires_at
)
INSERT INTO guest_identity_revocations(desktop_vmid,user_id,guest_username,reason)
SELECT desktop_vmid,user_id,guest_username,CASE WHEN session_expires_at <= now() THEN 'expired' ELSE 'access_removed' END FROM affected;

-- Login attempts are independent of the durable account/lease generation.
-- Zero means no fenced login has been reserved; never infer an old password's
-- version from an existing desktop or a successful Helper observation.
ALTER TABLE agent_guest_sessions ADD COLUMN login_generation bigint NOT NULL DEFAULT 0 CHECK (login_generation >= 0);

-- Drain pre-fence active sessions on upgrade, retaining their immutable UID/SID
-- and original lease epoch for exact account cleanup. Existing lease triggers
-- allocate the closing authority epoch and persist both revocation queues.
UPDATE desktop_leases l SET state='revoked',updated_at=now()
WHERE l.state='active' AND EXISTS(SELECT 1 FROM agent_guest_sessions g WHERE g.lease_id=l.id AND g.state IN ('ready','provisioning'));
UPDATE agent_guest_sessions SET state='revoking',updated_at=now() WHERE state IN ('ready','provisioning');
INSERT INTO desktop_computer_revocations(desktop_vmid,control_epoch)
SELECT DISTINCT g.desktop_vmid,e.control_epoch FROM agent_guest_sessions g JOIN desktop_computer_epochs e USING(desktop_vmid)
WHERE g.state='revoking'
ON CONFLICT(desktop_vmid) DO UPDATE SET requested_revision=desktop_computer_revocations.requested_revision+1,
 control_epoch=GREATEST(desktop_computer_revocations.control_epoch,excluded.control_epoch),updated_at=now();

ALTER TABLE agent_guest_sessions ADD CONSTRAINT agent_guest_ready_login CHECK (state<>'ready' OR login_generation>0);

CREATE FUNCTION protect_agent_login_generation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.lease_id=OLD.lease_id THEN
    IF NEW.control_epoch<>OLD.control_epoch OR NEW.generation<>OLD.generation OR NEW.login_generation<OLD.login_generation OR
       NEW.login_generation-OLD.login_generation>1 THEN
      RAISE EXCEPTION 'Agent login version cannot regress or skip' USING ERRCODE='23514';
    END IF;
    IF NEW.login_generation>OLD.login_generation AND
       (OLD.state NOT IN ('provisioning','ready') OR NEW.state<>'provisioning' OR NEW.session_id<>'' OR NEW.instance_id<>'' OR
        (NEW.guest_uid=0 AND NEW.guest_sid='')) THEN
      RAISE EXCEPTION 'Agent login requires a pre-bound account' USING ERRCODE='23514';
    END IF;
  ELSE
    IF OLD.state<>'disabled' OR NEW.state<>'provisioning' OR NEW.login_generation<>0 OR
       NEW.control_epoch<=OLD.control_epoch OR NEW.generation-OLD.generation<>1 THEN
      RAISE EXCEPTION 'Agent lease replacement requires completed cleanup' USING ERRCODE='23514';
    END IF;
  END IF;
  RETURN NEW;
END;
$$;
CREATE TRIGGER agent_guest_login_generation_guard BEFORE UPDATE ON agent_guest_sessions
FOR EACH ROW EXECUTE FUNCTION protect_agent_login_generation();

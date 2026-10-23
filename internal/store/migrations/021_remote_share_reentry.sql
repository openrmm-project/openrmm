-- Guest invitations a guest may come back to (docs/08-remote-access.md).
--
-- A link is single-use: the first correct code burns it. A guest who closes
-- the tab by mistake then needs a new invitation. When the technician allows
-- it, the same link and code work again until the invitation expires; each
-- use binds a new credential, ends the guest's previous one, and waits for the
-- technician's decision again.
--
--   remote_shares.reusable       the technician allowed coming back; off by
--                                default, so existing links stay single-use
--   remote_shares.redeem_count   how many times the code was accepted, so the
--                                technician sees that a guest is coming back
ALTER TABLE remote_shares ADD COLUMN IF NOT EXISTS reusable BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE remote_shares ADD COLUMN IF NOT EXISTS redeem_count INTEGER NOT NULL DEFAULT 0;

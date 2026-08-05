-- Editing a user's email address becomes its own permission.
--
-- users.email is where a password reset link is delivered, which makes it an
-- authentication factor rather than a contact detail: whoever can rewrite it can
-- request a reset and take over the account, 2FA aside. users.manage is not a
-- fine enough gate for that — a delegated user manager should be able to create
-- and scope technicians without holding an account takeover — and neither is
-- self-service, which is what it used to be.
INSERT INTO role_permissions (role_key, permission)
VALUES ('admin', 'users.email.manage')
ON CONFLICT DO NOTHING;

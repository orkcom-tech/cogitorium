-- Clients (web, desktop, later TUI) log in with a username and password
-- and receive a token; the token stays the only credential on the wire.
-- Empty means no password set — the seeded admin on a loopback install
-- never needs one.
ALTER TABLE users ADD COLUMN password_hash TEXT NOT NULL DEFAULT '';

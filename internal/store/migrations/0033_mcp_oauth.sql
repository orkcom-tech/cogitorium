-- An operator signs in to a remote MCP server, instead of pasting a token.
--
-- READ THIS BEFORE THE COLUMNS. Every other credential in this product is a
-- NAME: a gear's env_names, an MCP server's header_names, all resolved at the
-- moment of use from a store the operator filled in. That works because the
-- operator has the value. OAuth is the case where they do not and cannot: the
-- token is minted by somebody else's authorization server, arrives at this
-- install through a browser redirect, expires, and is replaced by a refresh
-- this server performs on its own. There is no name to resolve, so for the
-- first time this schema holds a live credential.
--
-- That is why the values here are ENCRYPTED with the same key and the same AEAD
-- the secrets table uses, rather than stored as text beside a comment promising
-- they are safe. A row here without COGITORIUM_SECRET_KEY cannot be written at
-- all — see the refusal in internal/mcpoauth — because writing somebody's
-- refresh token to disk in the clear is worse than not supporting the flow.

CREATE TABLE mcp_oauth (
    -- One grant per server. A second one would be a second identity for the
    -- same integration, and nothing in the product could say which was meant.
    server_id INTEGER NOT NULL PRIMARY KEY REFERENCES mcp_servers (id) ON DELETE CASCADE,

    -- The authorization server this grant belongs to, exactly as its metadata
    -- document spelled it. Kept because RFC 9207 validation compares the `iss`
    -- of a callback against it by simple string comparison — no case folding,
    -- no trailing-slash normalisation — so a normalised copy would be useless.
    issuer TEXT NOT NULL,
    -- Where the token and authorization endpoints are, from that same document.
    -- Stored rather than rediscovered on every refresh: a refresh at three in
    -- the morning should not depend on a metadata endpoint being up.
    authorize_url TEXT NOT NULL DEFAULT '',
    token_url     TEXT NOT NULL DEFAULT '',
    -- The registration this install holds with that authorization server, from
    -- dynamic client registration or configured by hand.
    client_id     TEXT NOT NULL DEFAULT '',
    client_secret TEXT NOT NULL DEFAULT '',

    -- The tokens. SEALED, never plaintext, with the key id beside them so a
    -- rotated key produces an honest "this was sealed with a key this install
    -- no longer has" rather than a corrupt decrypt.
    access_token  TEXT NOT NULL DEFAULT '',
    refresh_token TEXT NOT NULL DEFAULT '',
    key_id        TEXT NOT NULL DEFAULT '',

    -- When the access token stops working, in UTC. Empty means the server did
    -- not say, which is not the same as "never expires" — it means the only way
    -- to find out is a 401, and the code treats it that way.
    expires_at TEXT NOT NULL DEFAULT '',
    -- What was granted, space separated. Kept because a step-up has to ask for
    -- the UNION of what is held and what a 403 demanded; asking for only the
    -- new scope silently drops the old ones.
    scopes TEXT NOT NULL DEFAULT '',
    -- The canonical URI this token was minted FOR (RFC 8707). A token is bound
    -- to one resource, and sending it anywhere else is the confused-deputy
    -- attack the resource parameter exists to prevent.
    resource TEXT NOT NULL DEFAULT '',

    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

-- A flow in progress: what was sent to the browser, waiting for a callback.
--
-- SEPARATE FROM THE GRANT because it is not one. Until a callback arrives and
-- validates, there is no credential — only a promise that one might come back —
-- and mixing the two would mean a half-finished sign-in looked like a grant
-- with empty tokens. It is also short-lived, and a table that is swept is a
-- different thing from one that is kept.
CREATE TABLE mcp_oauth_pending (
    -- The `state` parameter, and the primary key: it is the only thing tying a
    -- callback to the request that started it, so it is generated with a CSPRNG
    -- and looked up exactly once.
    state     TEXT NOT NULL PRIMARY KEY,
    server_id INTEGER NOT NULL REFERENCES mcp_servers (id) ON DELETE CASCADE,

    -- The PKCE verifier. Sealed like a token, because it is one for the length
    -- of this flow: whoever holds it and the code can complete the exchange.
    verifier TEXT NOT NULL,
    key_id   TEXT NOT NULL DEFAULT '',

    -- What the client recorded BEFORE redirecting, which is what makes the
    -- validation on the way back mean anything. RFC 9207 says the expected
    -- issuer must come from the validated metadata document rather than from
    -- the callback itself; a value taken from an unvalidated source protects
    -- against nothing.
    issuer        TEXT NOT NULL,
    authorize_url TEXT NOT NULL DEFAULT '',
    token_url     TEXT NOT NULL,
    client_id     TEXT NOT NULL,
    client_secret TEXT NOT NULL DEFAULT '',
    scopes        TEXT NOT NULL DEFAULT '',
    resource      TEXT NOT NULL,
    redirect_uri  TEXT NOT NULL,
    -- Whether that authorization server said it sends `iss`. The RFC's table
    -- turns on this: advertised and absent is a rejection, not advertised and
    -- absent is fine.
    iss_advertised INTEGER NOT NULL DEFAULT 0,

    created_at TEXT NOT NULL
);

-- The sweep reads this. A pending flow nobody finished is a row holding a PKCE
-- verifier, and one that lived forever would be an ever-growing table of them.
CREATE INDEX idx_mcp_oauth_pending_age ON mcp_oauth_pending (created_at);

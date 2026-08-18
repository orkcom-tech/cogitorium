import { useState } from "react";
import { setup } from "../api";
import { session } from "../session";

// The first thing a new install shows, in place of the sign-in form.
//
// Nobody has a password yet — the seeded admin was created with a name, a role
// and a token, and no person has typed anything — so asking to be let in would
// be asking for something that does not exist. This asks for the password
// instead, and signs the operator in with what they just chose.
//
// On a server it also asks for the token this server printed when it started,
// because on an address the network can reach, an unauthenticated claim is a
// takeover waiting for a port scan. On somebody's own machine it does not,
// since anyone who can reach it can already read the database.
export default function SetupPage({
  local,
  onSignedIn,
}: {
  local: boolean;
  onSignedIn: () => void;
}) {
  const [password, setPassword] = useState("");
  const [again, setAgain] = useState("");
  const [token, setToken] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const mismatch = again !== "" && again !== password;
  const tooShort = password !== "" && password.length < 8;
  const ready =
    password.length >= 8 && again === password && (local || token !== "");

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    setup
      .claim(password, token)
      .then((r) => {
        session.keep(r.token);
        onSignedIn();
      })
      .catch((err: Error) => setError(err.message))
      .finally(() => setBusy(false));
  };

  return (
    <div className="login">
      <form className="card login-card" onSubmit={submit}>
        <h1 className="brand">Cogitorium</h1>
        <p className="hint">
          Nobody has signed in to this install yet. Choose the password for the{" "}
          <strong>admin</strong> account — it is how you will get in from now
          on.
        </p>
        {!local && (
          <label className="field">
            <span className="muted">admin token</span>
            <input
              required
              autoFocus
              value={token}
              onChange={(e) => setToken(e.target.value)}
              placeholder="printed in this server's log at startup"
            />
          </label>
        )}
        <label className="field">
          <span className="muted">password</span>
          <input
            required
            autoFocus={local}
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </label>
        <label className="field">
          <span className="muted">password again</span>
          <input
            required
            type="password"
            value={again}
            onChange={(e) => setAgain(e.target.value)}
          />
        </label>
        {tooShort && <p className="muted">at least 8 characters</p>}
        {mismatch && <p className="error">those two do not match</p>}
        {error && <p className="error">{error}</p>}
        <button type="submit" disabled={busy || !ready}>
          {busy ? "setting up…" : "set the password and sign in"}
        </button>
        {!local && (
          <p className="hint">
            This server is reachable over the network, so setting the first
            password needs the admin token it printed when it started —
            otherwise whoever finds the port first would own this install.
          </p>
        )}
      </form>
    </div>
  );
}

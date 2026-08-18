import { useState } from "react";
import { auth, type User } from "../api";

// Changing your own password.
//
// The route behind it — PUT /api/v1/users/{id}/password — has been there since
// passwords were added, and allowed a person to change their own from the
// start. Nothing in the interface ever called it for yourself: the only
// password field in the app was in the form for creating somebody else. That
// was survivable while a local install signed you in without one. It is not
// now, so this is the missing half.
export default function ChangePassword({
  me,
  onDone,
}: {
  me: User;
  onDone: () => void;
}) {
  const [password, setPassword] = useState("");
  const [again, setAgain] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState(false);

  const mismatch = again !== "" && again !== password;
  const ready = password.length >= 8 && again === password;

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    auth
      .setPassword(me.id, password)
      .then(() => setDone(true))
      .catch((err: Error) => setError(err.message))
      .finally(() => setBusy(false));
  };

  return (
    <div className="modal-backdrop" onClick={onDone}>
      <div className="modal theme-dialog" onClick={(e) => e.stopPropagation()}>
        <h2>Change your password</h2>
        {done ? (
          <>
            {/* The sessions already signed in are deliberately left alone.
             * Ending them here would sign this person out of the tab they are
             * standing in, which is the opposite of what they just asked for;
             * a stolen session is revoked by signing out, which is its own
             * button. */}
            <p className="muted">
              Done. Your other signed-in devices keep working — sign out on them
              to end those.
            </p>
            <div className="row">
              <span className="spacer" />
              <button onClick={onDone}>close</button>
            </div>
          </>
        ) : (
          <form onSubmit={submit}>
            <label className="field">
              <span className="muted">new password</span>
              <input
                required
                autoFocus
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
              />
            </label>
            <label className="field">
              <span className="muted">new password again</span>
              <input
                required
                type="password"
                value={again}
                onChange={(e) => setAgain(e.target.value)}
              />
            </label>
            {password !== "" && password.length < 8 && (
              <p className="muted">at least 8 characters</p>
            )}
            {mismatch && <p className="error">those two do not match</p>}
            {error && <p className="error">{error}</p>}
            <div className="row">
              <span className="spacer" />
              <button type="button" onClick={onDone}>
                cancel
              </button>
              <button type="submit" disabled={busy || !ready}>
                {busy ? "saving…" : "change it"}
              </button>
            </div>
          </form>
        )}
      </div>
    </div>
  );
}

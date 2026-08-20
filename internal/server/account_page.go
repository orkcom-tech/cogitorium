package server

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/orkcom-tech/cogitorium/internal/view"
)

// The signed-in person's own screen.
//
// The rail a server-rendered page draws had no account on it, so from the
// screen somebody lands on after signing in there was no way to sign out or
// change a password — both lived in a menu only the client can draw, on four
// screens out of twenty. This is the same two things, as a page.

func (s *Server) handleAccountPage(w http.ResponseWriter, r *http.Request) {
	s.renderAccount(w, r, "", "")
}

func (s *Server) renderAccount(w http.ResponseWriter, r *http.Request, problem, notice string) {
	caller := callerFrom(r.Context())
	s.renderPage(w, r, "cog.page.account", "cog.frag.password", "Account", view.Account{
		Ctx:     s.viewCtx(r, caller),
		Name:    caller.Name,
		IsAdmin: caller.IsAdmin(),
		Problem: problem,
		Notice:  notice,
	})
}

// handleAccountPasswordForm changes the signed-in person's own password.
//
// It asks for the current one, which the client's dialog did not. A session
// proves somebody sat down at this machine; it does not prove they are who the
// account belongs to, and an unlocked screen was enough to take an account
// over permanently.
func (s *Server) handleAccountPasswordForm(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderAccount(w, r, "that form could not be read", "")
		return
	}
	caller := callerFrom(r.Context())

	current := r.PostFormValue("current")
	next := r.PostFormValue("password")
	switch {
	case next != r.PostFormValue("again"):
		s.renderAccount(w, r, "those two do not match", "")
		return
	case len(next) < 8:
		s.renderAccount(w, r, "a password needs at least eight characters", "")
		return
	}

	// Checked by signing in as this person. Reusing Login rather than reaching
	// for the hash directly keeps one place that knows how a password is
	// verified, and that place already handles every stored form.
	if _, _, err := s.identity.Login(r.Context(), caller.Name, current); err != nil {
		s.renderAccount(w, r, "that is not your current password", "")
		return
	}
	if err := s.identity.SetPassword(r.Context(), caller.ID, next); err != nil {
		s.renderAccount(w, r, err.Error(), "")
		return
	}

	// The other sessions are deliberately left alone. Ending them here would
	// sign this person out of the tab they are standing in, which is the
	// opposite of what they just asked for; a stolen session is ended by
	// signing out, which is its own button below.
	s.renderAccount(w, r, "", "Changed. Your other signed-in devices keep working.")
}

// handleAccountSignOutForm ends this session and returns to the door.
func (s *Server) handleAccountSignOutForm(w http.ResponseWriter, r *http.Request) {
	token := sessionToken(r)
	clearSession(w, r)
	if token != "" {
		if err := s.identity.Logout(r.Context(), token); err != nil && !errors.Is(err, http.ErrNoCookie) {
			// Already gone is the ordinary case for a second press, and
			// refusing to finish signing out over it would leave somebody
			// looking at a screen they asked to leave.
			_ = err
		}
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleLookForm changes the appearance from the rail.
//
// A cookie, set here and read by viewCtx, because these screens carry no
// application JavaScript: each choice in the menu is its own small form, and
// the answer is the page it was pressed on.
//
// One field at a time — a mode or a colour — so choosing a colour does not
// silently also decide the ground. Whichever is not being set keeps its value.
//
// Both are read as a closed list rather than as whatever arrived: this ends up
// in an attribute and a style on <html>, and a cookie is written by whoever
// holds the browser.
func (s *Server) handleLookForm(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/workspaces", http.StatusSeeOther)
		return
	}
	mode, accent := look(r)

	if v := r.PostFormValue("mode"); v != "" {
		if v != "light" && v != "dark" {
			v = ""
		}
		mode = v
	}
	if v := r.PostFormValue("accent"); v != "" {
		accent = ""
		for _, a := range view.Accents {
			if strings.EqualFold(a.Hex, v) {
				accent = a.Hex
				break
			}
		}
	}

	if mode == "" && accent == "" {
		// No choice at all is the absence of the cookie, not a cookie saying
		// so — otherwise "follow this machine" would be a stored preference
		// that outlives somebody clearing their browser.
		http.SetCookie(w, &http.Cookie{Name: "cogitorium_look", Value: "", Path: "/", MaxAge: -1})
	} else {
		http.SetCookie(w, &http.Cookie{
			Name:   "cogitorium_look",
			Value:  url.QueryEscape(`{"mode":"` + mode + `","accent":"` + accent + `"}`),
			Path:   "/",
			MaxAge: 31536000, SameSite: http.SameSiteLaxMode,
		})
	}

	// Back where they were, so changing the look does not also move them.
	back := r.Header.Get("Referer")
	if back == "" || (!strings.HasPrefix(back, "/") && !strings.Contains(back, r.Host)) {
		back = "/workspaces"
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

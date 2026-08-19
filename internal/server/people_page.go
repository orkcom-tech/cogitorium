package server

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/orkcom-tech/cogitorium/internal/identity"
	"github.com/orkcom-tech/cogitorium/internal/view"
)

// Accounts, and the groups a workspace can be shared with.
//
// The access map that shares this screen stays where it is: it is a drawn
// graph, and a template renders a thing that exists at a moment rather than a
// layout somebody drags. The lists are the half that answers "who can reach
// what" in words, and words are what a template is for.

func (s *Server) handlePeoplePage(w http.ResponseWriter, r *http.Request) {
	if !s.pageAdmin(w, r) {
		return
	}
	s.renderPeople(w, r, "", "", "")
}

func (s *Server) renderPeople(w http.ResponseWriter, r *http.Request, problem, issuedTo, issued string) {
	model := view.People{
		Ctx:      s.viewCtx(r, callerFrom(r.Context())),
		Error:    problem,
		IssuedTo: issuedTo,
		Issued:   issued,
	}
	me := callerFrom(r.Context())

	users, err := s.identity.ListUsers(r.Context())
	if err != nil {
		model.Error = err.Error()
		s.renderDrawer(w, r, "cog.page.people", func() any { return model })
		return
	}
	teams, _ := s.identity.ListTeams(r.Context())

	teamsOf := map[int64][]string{}
	for _, t := range teams {
		for _, m := range t.Members {
			teamsOf[m.ID] = append(teamsOf[m.ID], t.Name)
		}
	}
	for _, u := range users {
		model.Users = append(model.Users, view.Person{
			ID: u.ID, Name: u.Name, Role: u.Role,
			Admin: u.IsAdmin(), Teams: teamsOf[u.ID],
			// Nobody is offered a button to delete themselves: an install with
			// nobody who can administer it is an install nobody can fix.
			Self: u.ID == me.ID,
		})
	}

	for _, t := range teams {
		row := view.Team{ID: t.ID, Name: t.Name}
		in := map[int64]bool{}
		for _, m := range t.Members {
			in[m.ID] = true
			row.Members = append(row.Members, view.Person{ID: m.ID, Name: m.Name, Role: m.Role})
		}
		// Only people who are not in it. Offering somebody already there makes
		// "add" a button that does nothing.
		for _, u := range users {
			if !in[u.ID] {
				row.Candidates = append(row.Candidates, view.Person{ID: u.ID, Name: u.Name})
			}
		}
		model.Teams = append(model.Teams, row)
	}

	// A fragment, always: this is a panel inside a page the client owns.
	s.renderDrawer(w, r, "cog.page.people", func() any { return model })
}

// handleCreateUserForm adds an account and shows its token once.
func (s *Server) handleCreateUserForm(w http.ResponseWriter, r *http.Request) {
	if !s.pageAdmin(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderPeople(w, r, "that form could not be read", "", "")
		return
	}
	name := strings.TrimSpace(r.PostFormValue("name"))
	role := r.PostFormValue("role")
	if role == "" {
		role = identity.RoleMember
	}
	u, token, err := s.identity.CreateUser(r.Context(), name, role, r.PostFormValue("password"))
	if err != nil {
		s.renderPeople(w, r, err.Error(), "", "")
		return
	}
	// The only time it appears anywhere: only its hash is stored.
	s.renderPeople(w, r, "", u.Name, token)
}

func (s *Server) handleDeleteUserForm(w http.ResponseWriter, r *http.Request) {
	if !s.pageAdmin(w, r) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.renderPeople(w, r, "that is not a user", "", "")
		return
	}
	// Checked here rather than only drawn: a button a template did not render
	// is a button somebody can still post.
	if id == callerFrom(r.Context()).ID {
		s.renderPeople(w, r, "that is you — an install with nobody to administer it is one nobody can fix", "", "")
		return
	}
	if err := s.identity.DeleteUser(r.Context(), id); err != nil {
		s.renderPeople(w, r, err.Error(), "", "")
		return
	}
	s.renderPeople(w, r, "", "", "")
}

func (s *Server) handleCreateTeamForm(w http.ResponseWriter, r *http.Request) {
	if !s.pageAdmin(w, r) {
		return
	}
	_ = r.ParseForm()
	if _, err := s.identity.CreateTeam(r.Context(), strings.TrimSpace(r.PostFormValue("name"))); err != nil {
		s.renderPeople(w, r, err.Error(), "", "")
		return
	}
	s.renderPeople(w, r, "", "", "")
}

func (s *Server) handleDeleteTeamForm(w http.ResponseWriter, r *http.Request) {
	if !s.pageAdmin(w, r) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.renderPeople(w, r, "that is not a team", "", "")
		return
	}
	if err := s.identity.DeleteTeam(r.Context(), id); err != nil {
		s.renderPeople(w, r, err.Error(), "", "")
		return
	}
	s.renderPeople(w, r, "", "", "")
}

func (s *Server) handleAddTeamMemberForm(w http.ResponseWriter, r *http.Request) {
	if !s.pageAdmin(w, r) {
		return
	}
	teamID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.renderPeople(w, r, "that is not a team", "", "")
		return
	}
	_ = r.ParseForm()
	userID, err := strconv.ParseInt(r.PostFormValue("user_id"), 10, 64)
	if err != nil {
		s.renderPeople(w, r, "pick somebody to add", "", "")
		return
	}
	if err := s.identity.AddTeamMember(r.Context(), teamID, userID); err != nil {
		s.renderPeople(w, r, err.Error(), "", "")
		return
	}
	s.renderPeople(w, r, "", "", "")
}

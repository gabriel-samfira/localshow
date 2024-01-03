package controllers

import (
	"bytes"
	"context"
	"html/template"
	"net/http"
	"sync"
	"time"

	"github.com/gabriel-samfira/localshow/database"
)

func NewAPIController(ctx context.Context, db *database.SQLDatabase) (*APIController, error) {
	ctr := &APIController{
		db:  db,
		ctx: ctx,
	}
	ctr.updateStats()
	go ctr.loop()

	return ctr, nil
}

type APIController struct {
	db  *database.SQLDatabase
	ctx context.Context

	countries    Datapoints
	passwords    Datapoints
	users        Datapoints
	authAttempts Datapoints

	mux sync.Mutex
}

func (a *APIController) LandingPage(w http.ResponseWriter, r *http.Request) {
	a.mux.Lock()
	defer a.mux.Unlock()

	t, err := template.New("").Parse(tpl)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("I messed up..."))
		return
	}

	tplParams := TemplateParams{
		Countries:    a.countries,
		Passwords:    a.passwords,
		Users:        a.users,
		AuthAttempts: a.authAttempts,
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, tplParams); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("I messed up..."))
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write(buf.Bytes())
}

func (a *APIController) updateStats() {
	a.mux.Lock()
	defer a.mux.Unlock()
	countries, err := a.db.GetTopCountries(10)
	if err != nil {
		return
	}
	a.countries = Datapoints{}
	for _, val := range countries {
		a.countries.Labels = append(a.countries.Labels, val.Name)
		a.countries.Data = append(a.countries.Data, val.Count)
	}

	passwords, err := a.db.GetTopPasswords(10)
	if err != nil {
		return
	}
	a.passwords = Datapoints{}
	for _, val := range passwords {
		a.passwords.Labels = append(a.passwords.Labels, val.Name)
		a.passwords.Data = append(a.passwords.Data, val.Count)
	}

	users, err := a.db.GetTopUsers(10)
	if err != nil {
		return
	}
	a.users = Datapoints{}
	for _, val := range users {
		a.users.Labels = append(a.users.Labels, val.Name)
		a.users.Data = append(a.users.Data, val.Count)
	}

	authAttempts, err := a.db.GetLastAuthAttemptsByDay(30)
	if err != nil {
		return
	}

	a.authAttempts = Datapoints{}
	for _, val := range authAttempts {
		a.authAttempts.Labels = append(a.authAttempts.Labels, val.Name)
		a.authAttempts.Data = append(a.authAttempts.Data, val.Count)
	}
}

func (a *APIController) loop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer func() {
		ticker.Stop()
	}()

	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			a.updateStats()
		}
	}
}

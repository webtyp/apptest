//go:build !wasm

// Package apptest runs user stories end to end: the app's own server binary over a fresh database of its real engine,
// its real site (wasm included) and a real headless Chrome. No mocks, no SQL by hand.
//
// A story lives in the module that fulfils it; the app only lists which ones it runs:
//
//	// mjosefa-cms/tests/stories_test.go
//	//go:build stories
//	func TestStories(t *testing.T) {
//		apptest.Run(t, "..", trustedipstories.All.UsingGiven(trustedipstories.Given{}))
//	}
package apptest

import "testing"

// Story is one user story. ID is H-<DOMAIN>-<NN> (numbered per domain, never renumbered); Says is the story in one
// sentence, as the team reads it; Run plays it against the running app with the prior data G the app hands over.
type Story[G any] struct {
	ID   string
	Says string
	Run  func(app *RunningApp, given G)
}

// Stories are the stories of one module, all needing the same prior data G.
type Stories[G any] []Story[G]

// ReadyStory is a story bound to its prior data: what Run plays.
type ReadyStory struct {
	id, says string
	run      func(app *RunningApp)
}

// UsingGiven binds every story to the prior data the app provides.
func (s Stories[G]) UsingGiven(given G) []ReadyStory {
	out := make([]ReadyStory, len(s))
	for i, st := range s {
		st := st
		out[i] = ReadyStory{id: st.ID, says: st.Says, run: func(app *RunningApp) { st.Run(app, given) }}
	}
	return out
}

// Run compiles the app rooted at appRoot once (site, server, migration) and plays every story as the subtest
// TestX/<ID>, each one against its own empty database, migrated, with its own server. Everything is removed at the end.
func Run(t *testing.T, appRoot string, groups ...[]ReadyStory) {
	t.Helper()
	build := buildApp(t, appRoot)
	for _, g := range groups {
		for _, st := range g {
			st := st
			t.Run(st.id, func(t *testing.T) {
				t.Log(st.says)
				app := build.start(t)
				defer app.stop()
				st.run(app)
			})
		}
	}
}

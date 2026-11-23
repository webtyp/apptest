//go:build !wasm

package apptest

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"webtyp.com/devbrowser/cdproto/runtime"
	"webtyp.com/devbrowser/chromedp"
	"webtyp.com/widget"
)

// Session is one person at the app: a browser context of its own (its own cookies), so two sessions in one story are
// two people at two machines.
type Session struct {
	app   *RunningApp
	ctx   context.Context
	base  string // the app as this session's machine reaches it
	scope string // CSS prefix of the screen on show after OpenScreen: the chassis keeps every screen mounted
}

// NewSession opens a fresh browser context on this machine (loopback): nobody signed in.
func (a *RunningApp) NewSession() *Session {
	a.t.Helper()
	return a.newSession(a.base)
}

// NewSessionOnAnotherDevice opens a fresh browser context that reaches the app through this machine's network address,
// which the app sees as a second device. Without a network address the story is skipped, never faked.
func (a *RunningApp) NewSessionOnAnotherDevice() *Session {
	a.t.Helper()
	ip := networkAddress()
	if ip == "" {
		a.t.Skip("apptest: this machine has no network address to act as a second device")
	}
	return a.newSession("https://" + ip + ":" + a.port)
}

func (a *RunningApp) newSession(base string) *Session {
	a.t.Helper()
	ctx, _ := chromedp.NewContext(a.browser, chromedp.WithNewBrowserContext())
	if err := chromedp.Run(ctx); err != nil {
		a.t.Fatalf("apptest: new browser context: %v", err)
	}
	return &Session{app: a, ctx: ctx, base: base}
}

// OpenHome loads the app's front door ("/").
func (s *Session) OpenHome() *Session {
	s.app.t.Helper()
	s.do("open /", chromedp.Navigate(s.base+"/"))
	return s
}

// OpenScreen opens one of the app's screens by its id (view.Screen.ID): the menu links to "#<id>".
func (s *Session) OpenScreen(screenID string) *Session {
	s.app.t.Helper()
	s.do("open screen "+screenID, chromedp.Navigate(s.base+"/#"+screenID))
	// Every screen stays mounted (a list per screen); the one on show carries widget.Current. From here on, fields and
	// buttons are looked for inside it only.
	s.scope = `[` + widget.Current.Key() + `="` + widget.Current.Value() + `"] `
	s.waitFor("screen "+screenID+" on show", `document.querySelector(`+jsString(s.scope+"*")+`) !== null`)
	return s
}

// ExpectPageText waits until the page shows text (a saved name in a list, a message).
func (s *Session) ExpectPageText(text string) {
	s.app.t.Helper()
	s.waitFor("expect page text "+text, `document.body && document.body.innerText.indexOf(`+jsString(text)+`) >= 0`)
}

// StatusOf asks path of the app with this session's cookies (a GET from the page) and returns the HTTP status: how a
// story checks what the server answers this person, not only what the screen shows.
func (s *Session) StatusOf(path string) int {
	s.app.t.Helper()
	var status int
	ctx, cancel := context.WithTimeout(s.ctx, stepTimeout)
	defer cancel()
	err := chromedp.Run(ctx, chromedp.Evaluate(`fetch(`+jsString(path)+`, {credentials: "same-origin"}).then(r => r.status)`, &status,
		func(p *runtime.EvaluateParams) *runtime.EvaluateParams { return p.WithAwaitPromise(true) }))
	if err != nil {
		s.fail("status of "+path, err.Error())
	}
	return status
}

// FillField types text into the form field named after its model field (form renders name=<field>) and leaves the
// field, as a person moving on to the next one does: that is the commit forms save on.
func (s *Session) FillField(field, text string) *Session {
	s.app.t.Helper()
	sel := s.scope + `[name="` + field + `"]`
	s.do("fill "+field, chromedp.WaitVisible(sel, chromedp.ByQuery), chromedp.SendKeys(sel, text, chromedp.ByQuery),
		chromedp.Evaluate(`(function(){var el=document.querySelector(`+jsString(sel)+`);if(el){el.dispatchEvent(new Event("change",{bubbles:true}));el.blur()}})()`, nil))
	return s
}

// Submit presses the submit button of the form on screen.
func (s *Session) Submit() *Session {
	s.app.t.Helper()
	s.do("submit", chromedp.WaitVisible(submitButton, chromedp.ByQuery), chromedp.Click(submitButton, chromedp.ByQuery))
	return s
}

const submitButton = `[type="submit"]`

// Find points at every element wearing the widget class c (a component's typed class, never a hand-written selector).
func (s *Session) Find(c widget.Class) *Element {
	return &Element{s: s, sel: s.scope + "." + string(c), what: string(c)}
}

// FindButton points at the button named name (a control's own name attribute, e.g. crudview's "cv-crudtoggle").
func (s *Session) FindButton(name string) *Element {
	return &Element{s: s, sel: s.scope + `button[name="` + name + `"]`, what: "button " + name}
}

// FindField points at the form field named after its model field.
func (s *Session) FindField(field string) *Element {
	return &Element{s: s, sel: s.scope + `[name="` + field + `"]`, what: "field " + field}
}

// Element is what a story looks for on screen.
type Element struct {
	s         *Session
	sel, what string
}

// ExpectVisible waits until at least one such element is on screen.
func (e *Element) ExpectVisible() {
	e.s.app.t.Helper()
	e.s.do("expect visible "+e.what, chromedp.WaitVisible(e.sel, chromedp.ByQuery))
}

// ExpectGone waits until no such element is on the page.
func (e *Element) ExpectGone() {
	e.s.app.t.Helper()
	e.s.waitFor("expect gone "+e.what, `document.querySelectorAll(`+jsString(e.sel)+`).length === 0`)
}

// ExpectCount waits until exactly n such elements are on the page.
func (e *Element) ExpectCount(n int) {
	e.s.app.t.Helper()
	e.s.waitFor("expect "+itoa(n)+" × "+e.what, `document.querySelectorAll(`+jsString(e.sel)+`).length === `+itoa(n))
}

// ExpectAtLeast waits until n or more such elements are on the page.
func (e *Element) ExpectAtLeast(n int) {
	e.s.app.t.Helper()
	e.s.waitFor("expect at least "+itoa(n)+" × "+e.what, `document.querySelectorAll(`+jsString(e.sel)+`).length >= `+itoa(n))
}

// ExpectSomeText waits until the first such element shows non-empty text (a message the app wrote).
func (e *Element) ExpectSomeText() {
	e.s.app.t.Helper()
	e.s.waitFor("expect text in "+e.what, `(function(){var el=document.querySelector(`+jsString(e.sel)+`);return !!el && el.innerText.trim() !== ""})()`)
}

// ExpectState waits until the first such element carries the widget state st (widget.Open, widget.Selected…): the
// control finished reacting, so the next step does not race its redraw.
func (e *Element) ExpectState(st widget.State) {
	e.s.app.t.Helper()
	sel := e.sel + `[` + st.Key() + `="` + st.Value() + `"]`
	e.s.waitFor("expect "+e.what+" "+st.Key(), `document.querySelector(`+jsString(sel)+`) !== null`)
}

// Count is how many such elements the page has right now.
func (e *Element) Count() int {
	e.s.app.t.Helper()
	var n int
	ctx, cancel := context.WithTimeout(e.s.ctx, stepTimeout)
	defer cancel()
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.querySelectorAll(`+jsString(e.sel)+`).length`, &n)); err != nil {
		e.s.fail("count "+e.what, err.Error())
	}
	return n
}

// Click clicks the first such element. It clicks the element itself, not a screen point: a screen slides in when it
// opens, and a click at coordinates during the slide lands on whatever is there at that moment.
func (e *Element) Click() {
	e.s.app.t.Helper()
	var ok bool
	e.s.do("click "+e.what, chromedp.WaitVisible(e.sel, chromedp.ByQuery),
		chromedp.Evaluate(`(function(){var el=document.querySelector(`+jsString(e.sel)+`);if(!el)return false;el.click();return true})()`, &ok))
	if !ok {
		e.s.fail("click "+e.what, "element gone before the click")
	}
}

// do runs actions with the step timeout; on failure it saves a screenshot and the page's console errors and fails.
func (s *Session) do(step string, actions ...chromedp.Action) {
	s.app.t.Helper()
	ctx, cancel := context.WithTimeout(s.ctx, stepTimeout)
	defer cancel()
	if err := chromedp.Run(ctx, actions...); err != nil {
		s.fail(step, err.Error())
	}
}

// waitFor polls the boolean JS expression until it is true or the step times out.
func (s *Session) waitFor(step, expr string) {
	s.app.t.Helper()
	deadline := time.Now().Add(stepTimeout)
	for {
		var ok bool
		ctx, cancel := context.WithTimeout(s.ctx, stepTimeout)
		err := chromedp.Run(ctx, chromedp.Evaluate(expr, &ok))
		cancel()
		if err == nil && ok {
			return
		}
		if time.Now().After(deadline) {
			reason := "timed out"
			if err != nil {
				reason = err.Error()
			}
			s.fail(step, reason)
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// fail records what the page looked like: a PNG named after the story, the URL and the visible text.
func (s *Session) fail(step, reason string) {
	t := s.app.t
	t.Helper()
	var png []byte
	var url, text string
	ctx, cancel := context.WithTimeout(s.ctx, stepTimeout)
	_ = chromedp.Run(ctx, chromedp.Location(&url), chromedp.Evaluate(`document.body ? document.body.innerText : ""`, &text), chromedp.FullScreenshot(&png, 80))
	cancel()
	shot := ""
	if len(png) > 0 && os.MkdirAll(s.app.failDir, 0o755) == nil {
		shot = filepath.Join(s.app.failDir, strings.ReplaceAll(t.Name(), "/", "_")+".png")
		_ = os.WriteFile(shot, png, 0o644)
	}
	t.Fatalf("step %q failed: %s\nurl: %s\nscreenshot: %s\npage text:\n%s", step, reason, url, shot, text)
}

func jsString(s string) string {
	return "'" + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), "'", `\'`) + "'"
}

func itoa(n int) string { return strconv.Itoa(n) }

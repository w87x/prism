package consult

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"

	"prism/internal/settings"
)

// webProvider drives a chat website in a dedicated tab of PRISM's persistent browser profile, so the user
// signs in once by hand (or not at all, for sites with a free anonymous tier). Selectors come from the Site;
// whatever is missing falls back to generic detection. Every piece of page script lives in the js*
// constants below.
type webProvider struct{ site Site }

const (
	// Find the composer (site selector, else the first visible text box), mark it, report page state.
	jsPrepare = `(sel=>{
	  const vis=e=>{const r=e.getBoundingClientRect();return r.width>40&&r.height>16&&getComputedStyle(e).visibility!=='hidden'};
	  let el=null;
	  if(sel){el=[...document.querySelectorAll(sel)].find(vis)||null}
	  else{el=[...document.querySelectorAll('textarea,[contenteditable="true"],[role="textbox"]')].filter(vis).pop()||null}
	  document.querySelectorAll('[data-prism-in]').forEach(e=>e.removeAttribute('data-prism-in'));
	  if(el)el.setAttribute('data-prism-in','1');
	  return {input:!!el, blocked:/just a moment|attention required|verify you are human/i.test(document.title),
	          body:document.body?document.body.innerText:''}})`

	jsLogin = `(sel=>!!sel&&!!document.querySelector(sel)||location.pathname.startsWith('/auth'))`

	// Put the prompt in the marked composer. Textareas need the native setter or React ignores the value.
	jsFill = `(t=>{const el=document.querySelector('[data-prism-in]'); if(!el) return false; el.focus();
	  if(el.tagName==='TEXTAREA'||el.tagName==='INPUT'){
	    const proto=el.tagName==='TEXTAREA'?HTMLTextAreaElement.prototype:HTMLInputElement.prototype;
	    Object.getOwnPropertyDescriptor(proto,'value').set.call(el,t);
	    el.dispatchEvent(new Event('input',{bubbles:true})); return true}
	  document.execCommand('selectAll'); return document.execCommand('insertText',false,t)})`

	jsClick = `(sel=>{const b=document.querySelector(sel); if(!b||b.disabled||b.getAttribute('aria-disabled')==='true') return false; b.click(); return true})`

	// answer-selector mode: how many assistant messages, the last one's text, still generating?
	jsPoll = `((ans,stop)=>{const m=ans?[...document.querySelectorAll(ans)]:[]; const l=m[m.length-1];
	  return {n:m.length, gen:!!stop&&!!document.querySelector(stop), text:l?l.innerText:'',
	          body:ans?'':document.body.innerText}})`
)

type pageState struct {
	Input   bool   `json:"input"`
	Blocked bool   `json:"blocked"`
	Body    string `json:"body"`
}

type pollState struct {
	N    int    `json:"n"`
	Gen  bool   `json:"gen"`
	Text string `json:"text"`
	Body string `json:"body"`
}

func call(fn string, args ...string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = jsonStr(a)
	}
	return fn + "(" + strings.Join(q, ",") + ")"
}

func (w webProvider) ask(ctx context.Context, s *Service, _ settings.Consult, prompt string) (string, error) {
	site := w.site
	if s.Browser == nil {
		return "", errors.New("no browser available")
	}
	tab, err := s.Browser.Tab("consult-" + site.Name)
	if err != nil {
		return "", err
	}
	// every Run below uses jc, so cancelling the job aborts whatever the page is doing
	jc, cancel := context.WithCancel(tab)
	defer cancel()
	defer context.AfterFunc(ctx, cancel)()
	run := func(actions ...chromedp.Action) error {
		err := chromedp.Run(jc, actions...)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	poll := s.Poll
	if poll <= 0 {
		poll = 2 * time.Second
	}
	sleep := func(d time.Duration) error {
		select {
		case <-time.After(d):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	if err := run(chromedp.Navigate(site.URL)); err != nil { // a fresh page is a fresh chat
		return "", err
	}
	var st pageState
	for i := 0; ; i++ {
		if err := run(chromedp.Evaluate(call(jsPrepare, site.Input), &st)); err != nil {
			return "", err
		}
		var login bool
		if err := run(chromedp.Evaluate(call(jsLogin, site.Login), &login)); err != nil {
			return "", err
		}
		if st.Input && !login {
			break
		}
		if login || i >= 30 {
			return "", loginHint(site, st, login)
		}
		if err := sleep(time.Second); err != nil {
			return "", err
		}
	}
	var base pollState
	if err := run(chromedp.Evaluate(call(jsPoll, site.Answer, site.Stop), &base)); err != nil {
		return "", err
	}

	var ok bool
	if err := run(chromedp.Evaluate(jsFill+"("+jsonStr(prompt)+")", &ok)); err != nil || !ok {
		return "", fmt.Errorf("could not type into %s's composer (page layout changed?): %v", site.Label, err)
	}
	if site.Send == "" {
		if err := sleep(400 * time.Millisecond); err != nil {
			return "", err
		}
		if err := run(chromedp.KeyEvent(kb.Enter)); err != nil {
			return "", err
		}
	} else {
		sent := false
		for i := 0; i < 20 && !sent; i++ { // the send button enables a beat after the text lands
			if err := sleep(500 * time.Millisecond); err != nil {
				return "", err
			}
			if err := run(chromedp.Evaluate(call(jsClick, site.Send), &sent)); err != nil {
				return "", err
			}
		}
		if !sent {
			return "", fmt.Errorf("%s's send button never became available (rate limit, or the layout changed)", site.Label)
		}
	}

	// Done = the answer stopped changing for long enough and the site's "stop" control is gone. With an
	// answer selector a new message must also have appeared; without one, the whole page text is compared.
	stableNeeded := 2
	if site.Answer == "" || site.Stop == "" {
		quiet := s.Quiet
		if quiet <= 0 {
			quiet = 8 * time.Second
		}
		stableNeeded = max(3, int(quiet/poll)) // no explicit signal: be patient through thinking pauses
	}
	var last string
	stable := 0
	for {
		if err := sleep(poll); err != nil {
			return "", err
		}
		var p pollState
		if err := run(chromedp.Evaluate(call(jsPoll, site.Answer, site.Stop), &p)); err != nil {
			return "", err
		}
		cur, changed := p.Text, p.N > base.N
		if site.Answer == "" {
			cur, changed = p.Body, p.Body != base.Body
		}
		if changed && !p.Gen && strings.TrimSpace(cur) != "" && cur == last {
			if stable++; stable >= stableNeeded {
				if site.Answer == "" {
					return afterPrompt(cur, base.Body, prompt), nil
				}
				return cur, nil
			}
		} else {
			stable = 0
		}
		last = cur
	}
}

// afterPrompt picks the model's reply out of a whole-page text dump: everything after the echoed prompt when
// the page shows it, else whatever follows the text the page already had.
func afterPrompt(body, before, prompt string) string {
	tail := ""
	for _, l := range strings.Split(prompt, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			tail = l
		}
	}
	if len(tail) > 80 {
		tail = tail[len(tail)-80:]
	}
	if i := strings.LastIndex(body, tail); tail != "" && i >= 0 {
		return strings.TrimSpace(body[i+len(tail):])
	}
	n := 0
	for n < len(body) && n < len(before) && body[n] == before[n] {
		n++
	}
	return strings.TrimSpace(body[n:])
}

func loginHint(site Site, st pageState, login bool) error {
	switch {
	case st.Blocked:
		return fmt.Errorf("%s showed a bot check — turn Headless off in Settings → Browser and pass it once in the visible window", site.Label)
	case login:
		return fmt.Errorf("%s is not signed in — turn Headless off in Settings → Browser, open %s in the browser and sign in; the login stays in PRISM's browser profile", site.Label, site.URL)
	}
	return fmt.Errorf("the %s page never showed a text box (blocked, offline, needs sign-in, or set the Input selector in Settings)", site.Label)
}

func jsonStr(s string) string { b, _ := json.Marshal(s); return string(b) }

package consult

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"

	"prism/internal/settings"
)

// Completer is the slice of the LLM router that selector tuning needs.
type Completer interface {
	CompleteJSON(ctx context.Context, ref, system, user string, out any) error
}

// TuneReport is the outcome of Service.Tune: the selectors that worked (when OK) and a log of what happened.
type TuneReport struct {
	OK    bool                 `json:"ok"`
	Site  settings.ConsultSite `json:"site"` // only the selector fields are filled
	Steps []string             `json:"steps"`
	Error string               `json:"error,omitempty"`
}

const tuneProbe = "Reply with exactly one word: PONG"

const (
	// What can be typed into / clicked, and which buttons exist, with a suggested selector for each.
	jsSnapInputs = `(()=>{
	  const vis=e=>{const r=e.getBoundingClientRect();return r.width>8&&r.height>8&&getComputedStyle(e).visibility!=='hidden'&&getComputedStyle(e).display!=='none'};
	  const hashed=c=>/\d{3,}/.test(c)||/^[a-z]{1,3}[-_]?[A-Za-z0-9]{6,}$/.test(c)&&/[0-9]/.test(c);
	  const sel=e=>{
	    if(e.id&&/^[A-Za-z][\w-]*$/.test(e.id)&&document.querySelectorAll('#'+e.id).length===1)return '#'+e.id;
	    for(const a of ['data-testid','data-test-id','data-qa','aria-label','name','placeholder']){
	      const v=e.getAttribute(a); if(v&&!/["\\]/.test(v)){const s=e.tagName.toLowerCase()+'['+a+'="'+v+'"]'; if(document.querySelectorAll(s).length===1)return s}}
	    const cls=[...e.classList].filter(c=>/^[A-Za-z][\w-]{1,30}$/.test(c)&&!hashed(c)).slice(0,2);
	    return e.tagName.toLowerCase()+cls.map(c=>'.'+c).join('')};
	  const attrs=e=>{const o={};for(const a of ['id','role','type','name','placeholder','aria-label','data-testid','title','contenteditable','disabled'])
	    {const v=e.getAttribute(a); if(v!==null)o[a]=v.slice(0,60)} return o};
	  const row=e=>({sel:sel(e), matches:(()=>{try{return document.querySelectorAll(sel(e)).length}catch(_){return 0}})(), tag:e.tagName.toLowerCase(),
	    attrs:attrs(e), text:(e.innerText||'').trim().slice(0,40), cls:[...e.classList].filter(c=>!hashed(c)).slice(0,5).join(' ')});
	  const inputs=[...document.querySelectorAll('textarea,[contenteditable="true"],[role="textbox"],input[type="text"],input:not([type])')].filter(vis).slice(0,8).map(row);
	  const buttons=[...document.querySelectorAll('button,[role="button"],[type="submit"]')].filter(vis).slice(0,60).map(row);
	  return JSON.stringify({title:document.title,inputs,buttons})})()`

	// Leaf-ish elements whose text is just the needle, each with its ancestor chain: that is the answer bubble.
	jsSnapAnswer = `(needle=>{
	  const clean=s=>s.toLowerCase().replace(/[^a-z0-9]+/g,' ').trim();
	  const out=[]; const seen=new Set();
	  const w=document.createTreeWalker(document.body,NodeFilter.SHOW_TEXT);
	  while(w.nextNode()){
	    const n=w.currentNode; if(clean(n.textContent)!==clean(needle))continue;
	    let e=n.parentElement; if(!e||e.closest('textarea,[data-prism-in],script,style'))continue;
	    const chain=[]; for(let i=0;i<6&&e&&e!==document.body;i++,e=e.parentElement){
	      const a={};for(const k of e.getAttributeNames()){if(k==='class'||k==='style')continue; if(k.startsWith('data-')||k==='role'||k==='id'||k.startsWith('aria-'))a[k]=e.getAttribute(k).slice(0,50)}
	      chain.push({tag:e.tagName.toLowerCase(),cls:[...e.classList].filter(c=>!(/\d{3,}/.test(c))).slice(0,6).join(' '),attrs:a,text:(e.innerText||'').trim().slice(0,50)});
	    }
	    const key=JSON.stringify(chain); if(!seen.has(key)){seen.add(key);out.push(chain)}
	    if(out.length>=4)break;
	  }
	  return JSON.stringify(out)})`

	jsCount = `(sel=>{try{return [...document.querySelectorAll(sel)].filter(e=>{const r=e.getBoundingClientRect();return r.width>0&&r.height>0}).length}catch(_){return -1}})`

	jsLastText = `(sel=>{try{const m=[...document.querySelectorAll(sel)];const l=m[m.length-1];return l?l.innerText:''}catch(_){return ''}})`

	jsBodyTail = `(()=>document.body.innerText.slice(-700))()`
)

const tuneSys1 = `You help automate a chat website through CSS selectors. You get a JSON snapshot of the page's visible text boxes and buttons (each with a suggested selector "sel" and how many elements it matches).
Reply with ONLY JSON: {"input": "<css selector for the message composer (must match exactly one element)>", "send": "<css selector for the send/submit button, or \"\" if pressing Enter sends>", "why": "<one short sentence>"}
Prefer ids, data-testid and aria-label selectors; avoid hashed or generated class names. If the composer is a textarea, the send button is usually a nearby icon-only button that is disabled while the box is empty — if unsure, answer "" for send.`

const tuneSys2 = `You help automate a chat website through CSS selectors. A test message was sent and the page answered with the single word PONG. You get the ancestor chains (innermost first) of the elements holding that answer, and the buttons that appeared right after sending.
Reply with ONLY JSON: {"answer": "<css selector matching EVERY assistant reply bubble but never the user's own messages; the last match must contain the reply text>", "stop": "<css selector of the button that is shown only while the model is generating (a Stop/Cancel button) or \"\" if none appeared>", "why": "<one short sentence>"}
The answer selector should hug the whole message (a role/data attribute such as data-message-author-role, or a stable class) rather than an inner paragraph that may not exist for longer replies. Avoid hashed or generated class names.`

func (s *Service) lockFor(name string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.locks == nil {
		s.jobs, s.locks = map[int64]*Job{}, map[string]*sync.Mutex{}
	}
	l := s.locks[name]
	if l == nil {
		l = &sync.Mutex{}
		s.locks[name] = l
	}
	return l
}

// Tune works out a site's selectors with the help of an LLM: it opens the site, shows the model the page's
// text boxes and buttons, sends a probe message with the model's choices, shows it where the answer landed,
// then proves the result with a real round trip. Page content only ever reaches the model as data and what
// comes back is CSS that is checked against the live page, so a hostile page cannot make it do anything.
func (s *Service) Tune(ctx context.Context, name string) (*TuneReport, error) {
	if s.LLM == nil {
		return nil, errors.New("no language model configured to analyse the page with")
	}
	if s.Browser == nil {
		return nil, errors.New("no browser available")
	}
	cfg := s.cfg(ctx)
	site, err := findSite(cfg, name)
	if err != nil && !strings.Contains(err.Error(), "disabled") {
		return nil, err
	}
	lock := s.lockFor(name)
	lock.Lock()
	defer lock.Unlock()

	ctx, stop := context.WithTimeout(ctx, 8*time.Minute)
	defer stop()
	tab, err := s.Browser.Tab("consult-" + name)
	if err != nil {
		return nil, err
	}
	jc, cancel := context.WithCancel(tab)
	defer cancel()
	defer context.AfterFunc(ctx, cancel)()
	rep := &TuneReport{}
	logf := func(f string, a ...any) { rep.Steps = append(rep.Steps, fmt.Sprintf(f, a...)) }
	run := func(actions ...chromedp.Action) error {
		err := chromedp.Run(jc, actions...)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	eval := func(js string, out any) error { return run(chromedp.Evaluate(js, out)) }
	sleep := func(d time.Duration) error {
		select {
		case <-time.After(d):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	ref := "role:chat"
	feedback := ""

	for attempt := 1; attempt <= 2; attempt++ {
		fail := func(f string, a ...any) {
			feedback = fmt.Sprintf(f, a...)
			logf("attempt %d failed: %s", attempt, feedback)
		}

		if err := run(chromedp.Navigate(site.URL)); err != nil {
			return nil, err
		}
		var snapA string
		for i := 0; i < 25; i++ { // single-page apps need a moment to draw the composer
			if err := sleep(time.Second); err != nil {
				return nil, err
			}
			if err := eval(jsSnapInputs, &snapA); err != nil {
				return nil, err
			}
			if strings.Contains(snapA, `"inputs":[{`) {
				break
			}
		}
		if !strings.Contains(snapA, `"inputs":[{`) {
			rep.Error = "the page never showed a text box — it may need a sign-in or a bot check (turn Headless off in Settings → Browser and deal with it in the window)"
			return rep, nil
		}
		logf("attempt %d: page snapshot is %d bytes", attempt, len(snapA))

		var a1 struct{ Input, Send, Why string }
		user := "Site: " + site.URL + "\nPage snapshot:\n" + snapA
		if feedback != "" {
			user += "\n\nYour previous answer did not work: " + feedback + "\nTry a different approach."
		}
		if err := s.LLM.CompleteJSON(ctx, ref, tuneSys1, user, &a1); err != nil {
			return nil, fmt.Errorf("model: %w", err)
		}
		a1.Input, a1.Send = strings.TrimSpace(a1.Input), strings.TrimSpace(a1.Send)
		logf("model picked input %q send %q — %s", a1.Input, a1.Send, a1.Why)
		var n int
		if a1.Input == "" || eval(call(jsCount, a1.Input), &n) != nil || n != 1 {
			fail("input selector %q matched %d visible elements; it must match exactly one", a1.Input, n)
			continue
		}

		// probe: type the message and send it
		var st pageState
		if err := eval(call(jsPrepare, a1.Input), &st); err != nil || !st.Input {
			fail("input selector %q did not resolve to a typeable element", a1.Input)
			continue
		}
		var ok bool
		if err := eval(jsFill+"("+jsonStr(tuneProbe)+")", &ok); err != nil || !ok {
			fail("could not type into %q", a1.Input)
			continue
		}
		if err := sleep(500 * time.Millisecond); err != nil {
			return nil, err
		}
		if a1.Send == "" {
			if err := run(chromedp.KeyEvent(kb.Enter)); err != nil {
				return nil, err
			}
		} else {
			var clicked bool
			if err := eval(call(jsClick, a1.Send), &clicked); err != nil || !clicked {
				fail("send selector %q matched nothing clickable (or the button was disabled after typing)", a1.Send)
				continue
			}
		}
		if err := sleep(700 * time.Millisecond); err != nil {
			return nil, err
		}
		var snapGen string // buttons present while the model is generating: Stop shows up here
		if err := eval(jsSnapInputs, &snapGen); err != nil {
			return nil, err
		}

		// wait for the lone word PONG to appear somewhere on the page
		var chains string
		for i := 0; i < 90; i++ {
			if err := sleep(time.Second); err != nil {
				return nil, err
			}
			if err := eval(call(jsSnapAnswer, "PONG"), &chains); err != nil {
				return nil, err
			}
			if chains != "" && chains != "[]" {
				break
			}
		}
		if chains == "" || chains == "[]" {
			var tail string
			_ = eval(jsBodyTail, &tail)
			fail("no element holding just the word PONG appeared within 90s after sending (end of page text: %q)", tail)
			continue
		}
		if err := sleep(2 * time.Second); err != nil { // let streaming finish and Stop disappear
			return nil, err
		}
		_ = eval(call(jsSnapAnswer, "PONG"), &chains)
		var snapDone string
		_ = eval(jsSnapInputs, &snapDone)
		logf("answer found; %d bytes of structure for the model", len(chains))

		var a2 struct{ Answer, Stop, Why string }
		user = "Answer element chains:\n" + chains +
			"\n\nButtons visible while generating (compare with the later list; a button only in this one is the Stop button):\n" + snapGen +
			"\n\nButtons after the answer finished:\n" + snapDone
		if feedback != "" {
			user += "\n\nYour previous answer did not work: " + feedback
		}
		if err := s.LLM.CompleteJSON(ctx, ref, tuneSys2, user, &a2); err != nil {
			return nil, fmt.Errorf("model: %w", err)
		}
		a2.Answer, a2.Stop = strings.TrimSpace(a2.Answer), strings.TrimSpace(a2.Stop)
		logf("model picked answer %q stop %q — %s", a2.Answer, a2.Stop, a2.Why)
		var last string
		if a2.Answer == "" || eval(call(jsLastText, a2.Answer), &last) != nil || !strings.Contains(strings.ToUpper(last), "PONG") || strings.Contains(last, "exactly one word") {
			fail("answer selector %q: its last match had text %q, which should be just the reply PONG", a2.Answer, last)
			continue
		}
		if a2.Stop != "" {
			if eval(call(jsCount, a2.Stop), &n) != nil || n != 0 {
				fail("stop selector %q matches %d visible elements after the answer finished; it must match none when idle", a2.Stop, n)
				continue
			}
		}

		// prove it with the real thing, exactly as consult would run it
		cand := site
		cand.Input, cand.Send, cand.Answer, cand.Stop = a1.Input, a1.Send, a2.Answer, a2.Stop
		cand.Login = "" // a signed-out marker is not tuned: anonymous chat is a feature, not an error
		pctx, pcancel := context.WithTimeout(ctx, 2*time.Minute)
		got, err := webProvider{cand}.ask(pctx, s, cfg, tuneProbe)
		pcancel()
		if err != nil || !strings.Contains(strings.ToUpper(got), "PONG") {
			fail("verification round trip returned %q (%v)", got, err)
			continue
		}
		logf("verified: the site answered %q", strings.TrimSpace(got))
		rep.OK = true
		rep.Site = settings.ConsultSite{Input: a1.Input, Send: a1.Send, Answer: a2.Answer, Stop: a2.Stop}
		return rep, nil
	}
	rep.Error = "could not find working selectors: " + feedback
	return rep, nil
}

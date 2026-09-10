package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"quietport.app/quietport/internal/cryptobox"
	"quietport.app/quietport/internal/model"
)

// The one way a member reaches the agent: a "Share a folder" shortcut in QPSync that opens a local page with a single
// button. The page is served on loopback with a per-install token in the URL, so nothing else on the machine (or a
// web page in the browser) can mint links. Circle keys never leave the device: the link is sealed here.

const shareLinkName = "Share a folder"

func shareLinkPath() string {
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(SyncRoot(), shareLinkName+".url")
	case "darwin":
		return filepath.Join(SyncRoot(), shareLinkName+".webloc")
	default:
		return filepath.Join(SyncRoot(), shareLinkName+".desktop")
	}
}

// writeShareLink (re)writes the shortcut for the current port and token.
func writeShareLink(port int, token string) {
	u := fmt.Sprintf("http://127.0.0.1:%d/?t=%s", port, token)
	var body string
	switch runtime.GOOS {
	case "windows":
		body = "[InternetShortcut]\r\nURL=" + u + "\r\n"
	case "linux":
		body = "[Desktop Entry]\nType=Link\nName=Share a folder\nIcon=folder-remote\nURL=" + u + "\n"
	default:
		body = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict><key>URL</key><string>` + u + `</string></dict></plist>
`
	}
	_ = os.MkdirAll(SyncRoot(), 0o755)
	_ = os.WriteFile(shareLinkPath(), []byte(body), 0o600)
}

func removeShareLink() { _ = os.Remove(shareLinkPath()) }

var uiTmpl = template.Must(template.New("ui").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Share a folder</title>
<style>
body{margin:0;background:#f3f5f9;color:#0f1626;font:17px/1.5 "Instrument Sans","Helvetica Neue",Arial,sans-serif}
main{max-width:34rem;margin:0 auto;padding:3rem 1.5rem}
h1{font-size:1.9rem;letter-spacing:-.02em;margin:0 0 .4rem}
p{margin:0 0 1rem;color:#5b6478}
.card{background:#fff;border-radius:18px;padding:1.4rem;box-shadow:0 18px 40px rgba(15,22,38,.10),0 2px 6px rgba(15,22,38,.06)}
label{display:block;font-weight:600;margin:.9rem 0 .4rem}
.opt{display:flex;align-items:center;gap:.6rem;padding:.6rem .8rem;border:1px solid #dfe4ee;border-radius:12px;margin-bottom:.5rem;cursor:pointer}
.opt input{accent-color:#2457f5}
input[type=text]{width:100%;box-sizing:border-box;font:inherit;padding:.7rem .8rem;border:1px solid #dfe4ee;border-radius:12px}
button{font:inherit;font-weight:600;background:#2457f5;color:#fff;border:0;border-radius:999px;padding:.85rem 1.4rem;margin-top:1.1rem;cursor:pointer}
button.quiet{background:#eef0f5;color:#0f1626}
.link{font-family:"IBM Plex Mono",Menlo,monospace;font-size:.95rem;background:#0f1626;color:#eaf0ff;padding:.9rem 1rem;border-radius:12px;word-break:break-all;user-select:all}
.hint{font-size:.9rem;color:#5b6478;margin-top:.8rem}
.err{color:#b42318;font-weight:600;margin-top:.8rem}
</style></head><body><main>
<h1>Share a folder</h1>
<p>Pick a folder and type the person's name. You get a link to send them any way you like. It works once and expires in 24 hours.</p>
<div class="card">
{{if not .Circles}}<p>None of your folders can be shared from here. Ask the person who runs Quietport.</p>{{else}}
<form id="f" method="post" action="/invite">
<input type="hidden" name="t" value="{{.Token}}">
<label>Which folder?</label>
{{range $i, $c := .Circles}}<label class="opt"><input type="radio" name="circle" value="{{$c.ID}}" {{if eq $i 0}}checked{{end}}> {{$c.DisplayName}}</label>{{end}}
<label for="name">Who is it for?</label>
<input type="text" id="name" name="name" placeholder="Their name" maxlength="40" autocomplete="off">
<button type="submit">Create link</button>
</form>
<div id="out" hidden>
<label>Send this link</label>
<div class="link" id="link"></div>
<button class="quiet" type="button" id="copy">Copy link</button>
<p class="hint">Works once. Expires in 24 hours. When they open it, the folder appears on their computer.</p>
<button class="quiet" type="button" id="again">Make another</button>
</div>
<div class="err" id="err" hidden></div>
{{end}}
</div>
<div class="card" style="margin-top:1.2rem">
<form id="nf" method="post" action="/circles">
<input type="hidden" name="t" value="{{.Token}}">
<label for="nfname">Start a new folder</label>
<input type="text" id="nfname" name="name" placeholder="Name the folder" maxlength="40" autocomplete="off">
<button type="submit" class="quiet">Create folder</button>
<p class="hint">It appears in QPSync on this computer right away. Then pick it above to invite people.</p>
<div class="err" id="nferr" hidden></div>
</form>
</div>
{{range .Owned}}
<div class="card" style="margin-top:1.2rem">
<label>People in {{.DisplayName}}</label>
<div id="people-{{.ID}}" data-circle="{{.ID}}" class="people"><p class="hint">Loading…</p></div>
</div>
{{end}}
<p class="hint" style="margin-top:2rem"><a href="/remove?t={{.Token}}" style="color:#5b6478">Remove Quietport from this computer</a></p>
</main>
<script>
const f=document.getElementById('f');
if(f){f.addEventListener('submit',async e=>{e.preventDefault();const b=f.querySelector('button');b.disabled=true;b.textContent='Creating…';
const r=await fetch('/invite',{method:'POST',body:new FormData(f)});const j=await r.json();b.disabled=false;b.textContent='Create link';
const err=document.getElementById('err');if(!r.ok){err.textContent=j.error||'Something went wrong.';err.hidden=false;return}
err.hidden=true;f.hidden=true;document.getElementById('link').textContent=j.url;document.getElementById('out').hidden=false;});
document.getElementById('copy').addEventListener('click',async()=>{try{await navigator.clipboard.writeText(document.getElementById('link').textContent);document.getElementById('copy').textContent='Copied'}catch(e){}});
document.getElementById('again').addEventListener('click',()=>{document.getElementById('out').hidden=true;f.hidden=false;document.getElementById('copy').textContent='Copy link';document.getElementById('name').value=''});}
const T={{.Token}};
async function loadPeople(box){const cid=box.dataset.circle;const r=await fetch('/people?t='+T+'&circle='+cid);const j=await r.json();
 if(!r.ok){box.innerHTML='<p class="hint">'+(j.error||'Could not load')+'</p>';return}
 box.innerHTML='';j.forEach(p=>{const row=document.createElement('div');row.className='opt';row.style.justifyContent='space-between';
  row.innerHTML='<span>'+p.name.replace(/-[a-z2-7]{4}$/,'')+(p.self?' (you)':'')+' <small style="color:#5b6478">'+(p.devices||[]).length+' computer'+((p.devices||[]).length==1?'':'s')+'</small></span>';
  if(!p.self){const b=document.createElement('button');b.type='button';b.className='quiet';b.style.margin='0';b.style.padding='.4rem .8rem';b.textContent='Remove';
   b.onclick=async()=>{if(!confirm('Remove '+p.name.replace(/-[a-z2-7]{4}$/,'')+' from this folder? The folder gets a new key; this can take a while for big folders.'))return;b.disabled=true;b.textContent='Removing…';
    const fd=new FormData();fd.append('t',T);fd.append('circle',cid);fd.append('person',p.person_id);const rr=await fetch('/people/remove',{method:'POST',body:fd});const jj=await rr.json();
    alert(jj.result||jj.error||'Done');loadPeople(box)};row.appendChild(b)}
  box.appendChild(row)})}
document.querySelectorAll('.people').forEach(loadPeople);
const nf=document.getElementById('nf');
if(nf){nf.addEventListener('submit',async e=>{e.preventDefault();const b=nf.querySelector('button');b.disabled=true;b.textContent='Creating…';
 const r=await fetch('/circles',{method:'POST',body:new FormData(nf)});const j=await r.json();b.disabled=false;b.textContent='Create folder';
 const err=document.getElementById('nferr');if(!r.ok){err.textContent=j.error||'Something went wrong.';err.hidden=false;return}
 location.reload()});}
</script></body></html>`))

type uiCircle struct {
	ID          int64
	DisplayName string
}

// serveLocalUI runs the loopback page. It returns the port actually bound (the configured one when free).
func (a *Agent) serveLocalUI(ctx context.Context, port int, token string) int {
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			a.logf("local page: %v", err)
			return 0
		}
	}
	port = ln.Addr().(*net.TCPAddr).Port
	mux := http.NewServeMux()
	check := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Host != "127.0.0.1:"+strconv.Itoa(port) && r.Host != "localhost:"+strconv.Itoa(port) {
			http.Error(w, "forbidden", 403)
			return false
		}
		t := r.URL.Query().Get("t")
		if r.Method == "POST" {
			t = r.FormValue("t")
		}
		if t == "" || t != token {
			http.Error(w, "This link is out of date. Open \"Share a folder\" from your QPSync folder again.", 403)
			return false
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		return true
	}
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		if !check(w, r) {
			return
		}
		var cs []uiCircle
		for _, c := range a.store.Config().Circles {
			if !c.Removed && c.CanInvite && c.KeySealed != "" && !c.NeedsKey {
				cs = append(cs, uiCircle{c.ID, c.DisplayName})
			}
		}
		var owned []uiCircle
		for _, c := range a.store.Config().Circles {
			if !c.Removed && c.Owner && c.KeySealed != "" {
				owned = append(owned, uiCircle{c.ID, c.DisplayName})
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = uiTmpl.Execute(w, map[string]any{"Token": token, "Circles": cs, "Owned": owned})
	})
	mux.HandleFunc("POST /circles", func(w http.ResponseWriter, r *http.Request) {
		if !check(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		name, err := a.createCircle(r.Context(), strings.TrimSpace(r.FormValue("name")))
		if err != nil {
			w.WriteHeader(400)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"created": name})
	})
	mux.HandleFunc("GET /people", func(w http.ResponseWriter, r *http.Request) {
		if !check(w, r) {
			return
		}
		cid, _ := strconv.ParseInt(r.URL.Query().Get("circle"), 10, 64)
		w.Header().Set("Content-Type", "application/json")
		ppl, err := a.people(r.Context(), cid)
		if err != nil {
			w.WriteHeader(400)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(ppl)
	})
	mux.HandleFunc("POST /people/remove", func(w http.ResponseWriter, r *http.Request) {
		if !check(w, r) {
			return
		}
		cid, _ := strconv.ParseInt(r.FormValue("circle"), 10, 64)
		pid, _ := strconv.ParseInt(r.FormValue("person"), 10, 64)
		w.Header().Set("Content-Type", "application/json")
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
		defer cancel()
		res, err := a.removePerson(ctx, cid, pid)
		if err != nil {
			w.WriteHeader(400)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"result": res})
	})
	mux.HandleFunc("POST /invite", func(w http.ResponseWriter, r *http.Request) {
		if !check(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		cid, _ := strconv.ParseInt(r.FormValue("circle"), 10, 64)
		name := strings.TrimSpace(r.FormValue("name"))
		u, err := a.createInvite(r.Context(), cid, name)
		if err != nil {
			w.WriteHeader(400)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"url": u})
	})
	mux.HandleFunc("GET /remove", func(w http.ResponseWriter, r *http.Request) {
		if !check(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = removeTmpl.Execute(w, map[string]any{"Token": token, "Folder": SyncRoot()})
	})
	mux.HandleFunc("POST /remove", func(w http.ResponseWriter, r *http.Request) {
		if !check(w, r) {
			return
		}
		keep := r.FormValue("folder") != "delete"
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = removeTmpl.Execute(w, map[string]any{"Done": true, "Keep": keep, "Folder": SyncRoot()})
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		a.logf("uninstall requested from the local page (keep folder: %v)", keep)
		go func() {
			time.Sleep(1500 * time.Millisecond)
			UninstallSelf(a.ts, !keep)
		}()
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { <-ctx.Done(); _ = srv.Close() }()
	go func() { _ = srv.Serve(ln) }()
	a.logf("share page on 127.0.0.1:%d", port)
	return port
}

// createInvite seals this device's copy of the circle key under a fresh code and registers the invite with the hub.
func (a *Agent) createInvite(ctx context.Context, circleID int64, name string) (string, error) {
	var cs *CircleState
	for i := range a.store.Config().Circles {
		c := a.store.Config().Circles[i]
		if c.ID == circleID {
			cs = &c
		}
	}
	if cs == nil || cs.Removed || !cs.CanInvite {
		return "", fmt.Errorf("you cannot share that folder from here")
	}
	key, err := a.store.CircleKey(*cs)
	if err != nil {
		return "", fmt.Errorf("this folder's key is not available on this computer yet")
	}
	code := cryptobox.NewInviteCode()
	sealed, err := cryptobox.SealWithCode(code, []model.CircleKey{key})
	if err != nil {
		return "", err
	}
	resp, err := a.hub.CreateInvite(ctx, model.DeviceInviteRequest{CircleID: circleID, Name: name, CodeHash: cryptobox.HashToken(code), Prefix: code[:6], SealedKeys: sealed, TTL: "24h"})
	if err != nil {
		if he, ok := err.(*HubError); ok {
			return "", fmt.Errorf("%s", he.Msg)
		}
		return "", fmt.Errorf("the hub could not be reached; try again in a minute")
	}
	a.logf("invite created for circle %d (%s)", circleID, resp.Person)
	return resp.URL + code, nil
}

var removeTmpl = template.Must(template.New("rm").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Remove Quietport</title>
<style>
body{margin:0;background:#f3f5f9;color:#0f1626;font:17px/1.5 "Instrument Sans","Helvetica Neue",Arial,sans-serif}
main{max-width:34rem;margin:0 auto;padding:3rem 1.5rem}
h1{font-size:1.9rem;letter-spacing:-.02em;margin:0 0 .4rem}
p{margin:0 0 1rem;color:#5b6478}
.card{background:#fff;border-radius:18px;padding:1.4rem;box-shadow:0 18px 40px rgba(15,22,38,.10),0 2px 6px rgba(15,22,38,.06)}
.opt{display:flex;align-items:center;gap:.6rem;padding:.6rem .8rem;border:1px solid #dfe4ee;border-radius:12px;margin-bottom:.5rem;cursor:pointer}
button{font:inherit;font-weight:600;background:#b42318;color:#fff;border:0;border-radius:999px;padding:.85rem 1.4rem;margin-top:1.1rem;cursor:pointer}
a{color:#5b6478}
</style></head><body><main>
{{if .Done}}<h1>Quietport has been removed.</h1><p>{{if .Keep}}Your files are still in {{.Folder}}. They will not update any more.{{else}}The QPSync folder was deleted too.{{end}} You can close this page.</p>
{{else}}<h1>Remove Quietport from this computer?</h1><p>Your shared folders stop updating on this computer. Other people's copies are not affected.</p>
<div class="card"><form method="post" action="/remove">
<input type="hidden" name="t" value="{{.Token}}">
<label class="opt"><input type="radio" name="folder" value="keep" checked> Keep my files in {{.Folder}}</label>
<label class="opt"><input type="radio" name="folder" value="delete"> Delete the QPSync folder too</label>
<button type="submit">Remove Quietport</button>
</form></div>
<p style="margin-top:1.4rem"><a href="/?t={{.Token}}">Never mind, go back</a></p>{{end}}
</main></body></html>`))

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"quietport.app/quietport/internal/cryptobox"
	"quietport.app/quietport/internal/model"
)

// Joining: a computer that already has Quietport redeems an invite link for the person it belongs to. The hub adds
// the person to the link's folders and hands back the keys the inviter sealed under the code; this computer opens
// them with the code, keeps every folder it already had, and seals the keys on to the person's other computers,
// which the hub cannot do. Nothing is reinstalled and nothing is enrolled (docs/adr/0019).

var inviteCodeRe = regexp.MustCompile(`[a-z2-7]{26}`)

// codeFromLink pulls the 26-character code out of a pasted link, or a bare code.
func codeFromLink(s string) string {
	return inviteCodeRe.FindString(strings.ToLower(strings.TrimSpace(s)))
}

// joinCircles folds the folders a join returned into the config. It reports the directory of every joined folder,
// the name a member finds in the sync root, and the slugs of the circles whose directory is new to them, which must
// be emptied before they sync (Config.placeFolders, emptyNewFolders, docs/adr/0021). A folder that is
// not there is added. A folder already there keeps its place and its directory; it takes the hub's current settings
// and storage credentials, as every heartbeat's bundle does, and a resync is scheduled only when something that
// matters to the ciphertext changed: it was added, it had been removed, or its key changed. A key from the link
// replaces what the folder holds only when the folder has none or the link's is newer: a link sealed before a re-key
// never downgrades a working folder. A key is stored for the generation it was sealed for, so a stale link on a
// folder without a key leaves it still needing one. A folder whose key did not arrive at all is listed and marked,
// never dropped: the hub already counts this person a member, and the next link for it heals it. A folder that
// arrives never shares a directory with one already here.
func joinCircles(c *Config, circles []model.CircleConfig, keys []model.CircleKey, seal func(any) (string, error)) (names, fresh []string) {
	names = []string{}
	arriving := map[string]bool{}
	for _, cc := range circles {
		idx := -1
		for i := range c.Circles {
			if c.Circles[i].Slug == cc.Slug {
				idx = i
			}
		}
		s3, _ := seal([2]string{cc.S3AccessKey, cc.S3SecretKey})
		changed := false
		if idx < 0 {
			c.Circles = append(c.Circles, CircleState{CircleConfig: cc, S3Sealed: s3})
			idx = len(c.Circles) - 1
			changed = true
			arriving[cc.Slug] = true
		}
		cs := &c.Circles[idx]
		cs.CircleConfig = cc
		cs.S3Sealed = s3
		if cs.Removed {
			cs.readmit()
			changed = true
			arriving[cc.Slug] = true
		}
		for _, k := range keys {
			if k.Slug != cc.Slug || (cs.KeySealed != "" && k.Generation <= cs.KeyGen) {
				continue
			}
			if sealed, err := seal(k); err == nil {
				cs.KeySealed, cs.KeyGen = sealed, k.Generation
				changed = true
			}
		}
		cs.NeedsKey = cs.KeySealed == "" || cs.KeyGen != cc.Generation
		if changed {
			cs.Resync = true
		}
	}
	fresh = c.placeFolders(arriving)
	for _, cc := range circles {
		for _, cs := range c.Circles {
			if cs.Slug == cc.Slug {
				names = append(names, cs.folder())
			}
		}
	}
	return names, fresh
}

// join redeems a link from the Share page (or the installer, through it) and returns the names of the folders now
// on this computer. Every failure is one plain sentence for the page.
func (a *Agent) join(ctx context.Context, link string) ([]string, error) {
	code := codeFromLink(link)
	if code == "" {
		return nil, errors.New("that does not look like a Quietport invite link")
	}
	var resp model.JoinResponse
	if err := a.hub.do(ctx, "POST", "/v1/join", model.JoinRequest{Code: code}, &resp); err != nil {
		if he, ok := err.(*HubError); ok {
			return nil, errors.New(he.Msg)
		}
		return nil, errors.New("the hub could not be reached; try again in a minute")
	}
	var keys []model.CircleKey
	if resp.SealedKeys != "" {
		if err := cryptobox.OpenWithCode(code, resp.SealedKeys, &keys); err != nil {
			a.logf("join: the keys under link %s did not open: %v", code[:6], err)
			keys = nil
		}
	}
	var names []string
	_ = a.store.Update(func(c *Config) {
		var fresh []string
		names, fresh = joinCircles(c, resp.Circles, keys, a.store.Seal)
		// under the store's lock, so no sync can read the new folder before its directory is emptied
		a.emptyNewFolders(c, fresh)
	})
	a.grantOwnDevices(ctx, resp.Circles, keys, resp.OtherDevices)
	a.refreshWatches()
	select {
	case a.syncNow <- "":
	default:
	}
	a.logf("joined %s with a link from %q (%d other computer(s) of this person)", strings.Join(names, ", "), resp.InviterName, len(resp.OtherDevices))
	if len(names) == 0 {
		return nil, errors.New("the folder in this link no longer exists")
	}
	if len(keys) == 0 {
		// a whole sentence, shown as it is by the Share page and by the installer
		return names, errors.New("The folder is here, but its key did not arrive. Ask " + orSomeone(resp.InviterName) + " for a new link.")
	}
	return names, nil
}

// grantOwnDevices seals each joined folder's key to the person's other computers, so they pick it up on their next
// heartbeat instead of reporting that they need a new invitation. Best effort: a failure here is logged, and a
// later link for the folder heals the computer that missed out.
func (a *Agent) grantOwnDevices(ctx context.Context, circles []model.CircleConfig, keys []model.CircleKey, devices []model.DeviceKey) {
	if len(devices) == 0 {
		return
	}
	for _, cc := range circles {
		var grants []model.KeyGrant
		for _, k := range keys {
			if k.Slug != cc.Slug {
				continue
			}
			for _, d := range devices {
				if d.PubKey == "" {
					continue
				}
				box, err := cryptobox.SealToDevice(d.PubKey, k)
				if err != nil {
					continue
				}
				grants = append(grants, model.KeyGrant{DeviceID: d.ID, CircleID: cc.ID, Generation: k.Generation, SealedBox: box})
			}
		}
		if len(grants) == 0 {
			continue
		}
		if err := a.hub.do(ctx, "POST", "/v1/circles/"+strconv.FormatInt(cc.ID, 10)+"/grants", grants, nil); err != nil {
			a.logf("%s: the key could not be passed to this person's other computers: %v", cc.Slug, err)
			continue
		}
		a.logf("%s: key passed to %d other computer(s) of this person", cc.Slug, len(grants))
	}
}

// JoinInstalled is the installer's path on a computer that already has Quietport: hand the link to the running
// agent through its Share page, which does the join in-process with the store it already holds. If the agent is
// not running it is started and given time to bring the mesh up; the page only opens once it has.
func JoinInstalled(link string) ([]string, error) {
	port, token, err := sharePage()
	if err != nil {
		return nil, errors.New("Quietport is on this computer but its settings could not be read.")
	}
	client := &http.Client{Timeout: 3 * time.Minute}
	probe := func(ctx context.Context) error {
		req, _ := http.NewRequestWithContext(ctx, "GET", "http://127.0.0.1:"+strconv.Itoa(port)+"/?t="+token, nil)
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			return errors.New(resp.Status)
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	if probe(ctx) != nil {
		_ = startAgent()
		if err := waitForDaemon(ctx, func(ctx context.Context) error {
			// the agent may bind a new port when it starts
			if p, t, err := sharePage(); err == nil {
				port, token = p, t
			}
			return probe(ctx)
		}, 2*time.Minute, 2*time.Second); err != nil {
			return nil, errors.New("Quietport is on this computer but is not running, so restart the computer and open the link again.")
		}
	}
	resp, err := client.PostForm("http://127.0.0.1:"+strconv.Itoa(port)+"/join", url.Values{"t": {token}, "link": {link}})
	if err != nil {
		return nil, errors.New("Quietport on this computer did not answer.")
	}
	defer resp.Body.Close()
	var out struct {
		Joined []string `json:"joined"`
		Note   string   `json:"note"`
		Error  string   `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out)
	if resp.StatusCode != 200 || len(out.Joined) == 0 {
		if out.Error == "" {
			out.Error = "something went wrong"
		}
		return nil, errors.New(out.Error)
	}
	if out.Note != "" {
		return out.Joined, errors.New(out.Note)
	}
	return out.Joined, nil
}

// sharePage reads the running agent's loopback port and token from the config file. They are the two fields that
// are not sealed, because the shortcut in QPSync carries them too. The store is deliberately not opened here: that
// would reach into the keychain from a second program, for two values that were never secret.
func sharePage() (int, string, error) {
	b, err := os.ReadFile(ConfigPath())
	if err != nil {
		return 0, "", err
	}
	var c struct {
		UIPort  int    `json:"ui_port"`
		UIToken string `json:"ui_token"`
	}
	if err := json.Unmarshal(b, &c); err != nil || c.UIPort == 0 || c.UIToken == "" {
		return 0, "", errors.New("no share page recorded")
	}
	return c.UIPort, c.UIToken, nil
}

func orSomeone(name string) string {
	if name == "" {
		return "the person who sent it"
	}
	return name
}

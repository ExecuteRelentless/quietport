package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"quietport.app/quietport/internal/cryptobox"
	"quietport.app/quietport/internal/model"
)

// Owner operations run from the member's own computer: list the people in a folder, remove one, and re-key the
// folder afterwards (FR-91 done by the device that holds the key instead of the operator).

type opKey struct {
	AccessKey  string `json:"access_key"`
	SecretKey  string `json:"secret_key"`
	Endpoint   string `json:"endpoint"`
	Bucket     string `json:"bucket"`
	UsedBytes  int64  `json:"used_bytes"`
	Generation int    `json:"generation"`
}

func (a *Agent) circleByID(id int64) (CircleState, bool) {
	for _, c := range a.store.Config().Circles {
		if c.ID == id {
			return c, true
		}
	}
	return CircleState{}, false
}

func (a *Agent) people(ctx context.Context, circleID int64) ([]model.CirclePerson, error) {
	var out []model.CirclePerson
	err := a.hub.do(ctx, "GET", "/v1/circles/"+strconv.FormatInt(circleID, 10)+"/people", nil, &out)
	return out, err
}

// removePerson takes someone out of the folder on the hub, then re-keys the folder from this computer.
func (a *Agent) removePerson(ctx context.Context, circleID, personID int64) (string, error) {
	cs, ok := a.circleByID(circleID)
	if !ok || !cs.Owner {
		return "", errors.New("you do not own this folder")
	}
	var remaining []model.CirclePerson
	if err := a.hub.do(ctx, "POST", "/v1/circles/"+strconv.FormatInt(circleID, 10)+"/remove", map[string]int64{"person_id": personID}, &remaining); err != nil {
		if he, ok := err.(*HubError); ok {
			return "", errors.New(he.Msg)
		}
		return "", err
	}
	took, err := a.rotate(ctx, circleID, remaining)
	if err != nil {
		return "", fmt.Errorf("removed, but the folder could not be re-keyed yet (%v). It will be retried; until then the old computer may still read new files", err)
	}
	// the switch also rotated the folder's storage credentials: fetch them now instead of at the next heartbeat
	a.heartbeat(ctx)
	select {
	case a.syncNow <- cs.Slug:
	default:
	}
	return fmt.Sprintf("Removed. The folder has a new key (took %s).", took.Round(time.Second)), nil
}

// rotate: new key, re-encrypt through this computer, seal to everyone still in, switch, purge the old copy.
func (a *Agent) rotate(ctx context.Context, circleID int64, people []model.CirclePerson) (time.Duration, error) {
	a.syncMu.Lock()
	defer a.syncMu.Unlock()
	cs, ok := a.circleByID(circleID)
	if !ok {
		return 0, errors.New("unknown folder")
	}
	oldKey, err := a.store.CircleKey(cs)
	if err != nil {
		return 0, errors.New("this computer has no key for the folder")
	}
	var k opKey
	path := "/v1/circles/" + strconv.FormatInt(circleID, 10)
	if err := a.hub.do(ctx, "POST", path+"/opkey", nil, &k); err != nil {
		return 0, err
	}
	defer a.hub.do(context.Background(), "DELETE", path+"/opkey/"+k.AccessKey, nil, nil)
	start := time.Now()
	newKey := model.CircleKey{Slug: cs.Slug, Generation: k.Generation + 1, Password: cryptobox.NewCircleSecret(), Salt: cryptobox.NewCircleSecret()}
	env := a.rc.envMulti(k.Bucket, k.AccessKey, k.SecretKey, k.Endpoint, map[string]model.CircleKey{"OLD": oldKey, "NEW": newKey})
	cmd := exec.CommandContext(ctx, a.rc.bin, "copy", "OLD:", "NEW:", "--transfers", "4", "--checkers", "8", "-q", "--retries", "5", "--color", "never")
	cmd.Env = env
	hideWindow(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		return 0, fmt.Errorf("re-encryption failed: %s", strings.TrimSpace(ScrubPaths(tailLines(string(out), 3))))
	}
	// grants for everyone still in, sealed here; the hub only relays them
	var grants []model.KeyGrant
	for _, p := range people {
		for _, d := range p.Devices {
			if d.PubKey == "" {
				continue
			}
			box, err := cryptobox.SealToDevice(d.PubKey, newKey)
			if err != nil {
				continue
			}
			grants = append(grants, model.KeyGrant{DeviceID: d.ID, CircleID: circleID, Generation: newKey.Generation, SealedBox: box})
		}
	}
	if len(grants) > 0 {
		if err := a.hub.do(ctx, "POST", path+"/grants", grants, nil); err != nil {
			return 0, err
		}
	}
	if err := a.hub.do(ctx, "POST", path+"/generation", map[string]int{"generation": newKey.Generation}, nil); err != nil {
		return 0, err
	}
	// this computer switches immediately; everyone else picks the new key up on their next heartbeat
	sealed, _ := a.store.Seal(newKey)
	_ = a.store.Update(func(c *Config) {
		for i := range c.Circles {
			if c.Circles[i].ID == circleID {
				c.Circles[i].KeySealed = sealed
				c.Circles[i].KeyGen = newKey.Generation
				c.Circles[i].Generation = newKey.Generation
				c.Circles[i].NeedsKey = false
				c.Circles[i].Resync = true
			}
		}
	})
	purge := exec.CommandContext(ctx, a.rc.bin, "purge", "S3:"+k.Bucket+"/g"+strconv.Itoa(k.Generation), "-q", "--color", "never")
	purge.Env = env
	hideWindow(purge)
	if out, err := purge.CombinedOutput(); err != nil {
		a.logf("%s: old ciphertext not fully removed: %s", cs.Slug, strings.TrimSpace(ScrubPaths(tailLines(string(out), 2))))
	}
	a.logf("%s: re-keyed to generation %d from this computer in %s (%d grants)", cs.Slug, newKey.Generation, time.Since(start).Round(time.Second), len(grants))
	return time.Since(start), nil
}

// createCircle: this computer generates the key for a brand-new folder and registers it with the hub.
func (a *Agent) createCircle(ctx context.Context, name string) (string, error) {
	var cc model.CircleConfig
	if err := a.hub.do(ctx, "POST", "/v1/circles", map[string]string{"name": name}, &cc); err != nil {
		if he, ok := err.(*HubError); ok {
			return "", errors.New(he.Msg)
		}
		return "", errors.New("the hub could not be reached; try again in a minute")
	}
	key := model.CircleKey{Slug: cc.Slug, Generation: cc.Generation, Password: cryptobox.NewCircleSecret(), Salt: cryptobox.NewCircleSecret()}
	sealed, err := a.store.Seal(key)
	if err != nil {
		return "", err
	}
	s3, _ := a.store.Seal([2]string{cc.S3AccessKey, cc.S3SecretKey})
	_ = a.store.Update(func(c *Config) {
		for i := range c.Circles {
			if c.Circles[i].ID == cc.ID { // a heartbeat got there first; give it the key
				c.Circles[i].KeySealed, c.Circles[i].KeyGen, c.Circles[i].Resync = sealed, cc.Generation, true
				return
			}
		}
		c.Circles = append(c.Circles, CircleState{CircleConfig: cc, KeySealed: sealed, KeyGen: cc.Generation, S3Sealed: s3, Resync: true})
	})
	_ = os.MkdirAll(CircleDir(cc.DisplayName), 0o755)
	a.refreshWatches()
	select {
	case a.syncNow <- cc.Slug:
	default:
	}
	a.logf("%s: new folder %q created from this computer", cc.Slug, cc.DisplayName)
	return cc.DisplayName, nil
}

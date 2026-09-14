package agent

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// AskForFullSync asks the running agent for one full sync of a folder, keeping this device's copies ("this"), the
// hub's ("hub") or the newer of each ("newer") where the two sides differ (docs/adr/0023). It reports the folder's
// name. Every failure is one plain sentence.
func AskForFullSync(folder, keep string) (string, error) {
	port, token, err := sharePage()
	if err != nil {
		return "", errors.New("Quietport is not set up on this computer.")
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.PostForm("http://127.0.0.1:"+strconv.Itoa(port)+"/resync", url.Values{"t": {token}, "folder": {folder}, "keep": {keep}})
	if err != nil {
		return "", errors.New("Quietport is not running on this computer, so start it and try again.")
	}
	defer resp.Body.Close()
	var out struct {
		Folder string `json:"folder"`
		Error  string `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out)
	if resp.StatusCode != 200 || out.Folder == "" {
		if out.Error == "" {
			out.Error = "something went wrong"
		}
		return "", errors.New(out.Error)
	}
	return out.Folder, nil
}

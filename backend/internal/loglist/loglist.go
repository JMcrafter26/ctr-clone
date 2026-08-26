package loglist

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type StaticCTEntry struct {
	Name              string `yaml:"name" json:"name"`
	SubmissionPrefix  string `yaml:"submission_prefix" json:"submission_prefix"`
	MonitoringPrefix  string `yaml:"monitoring_prefix" json:"monitoring_prefix"`
	PublicKey         string `yaml:"public_key" json:"public_key"`
	TemporalInterval  string `yaml:"temporal_interval,omitempty" json:"temporal_interval,omitempty"`
}

type Curated struct {
	StaticCT []StaticCTEntry `yaml:"static_ct"`
}

// LoadCurated loads logs.yaml
func LoadCurated(path string) (*Curated, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Curated
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// ChromeLogList v3 format (simplified)
type ChromeLogList struct {
	Operators []struct {
		Name string `json:"name"`
		Logs []struct {
			Description string `json:"description"`
			LogID       string `json:"log_id"`
			Key         string `json:"key"`
			URL         string `json:"url"`
			MMD         int    `json:"mmd"`
			State       struct {
				Usable    string `json:"usable"`
				ReadOnly  *struct{ Until *string `json:"until"` } `json:"readonly"`
				Retired   *struct{ Timestamp string `json:"timestamp"` } `json:"retired"`
			} `json:"state"`
			TemporalInterval *struct {
				StartInclusive string `json:"start_inclusive"`
				EndExclusive   string `json:"end_exclusive"`
			} `json:"temporal_interval"`
		} `json:"logs"`
	} `json:"operators"`
}

// FetchChrome fetches https://www.gstatic.com/ct/log_list/v3/log_list.json
func FetchChrome(client *http.Client) (*ChromeLogList, error) {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := client.Get("https://www.gstatic.com/ct/log_list/v3/log_list.json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("chrome log list: status %d", resp.StatusCode)
	}
	var out ChromeLogList
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AppleLogList simplified
type AppleLogList struct {
	Version string `json:"version"`
	Operators []struct {
		Name string `json:"name"`
		Logs []struct {
			Description string `json:"description"`
			LogID       string `json:"log_id"`
			Key         string `json:"key"`
			URL         string `json:"url"`
			LogType     string `json:"log_type"`
			DNS         string `json:"dns"`
			State       string `json:"state"`
			TemporalInterval *struct {
				StartInclusive string `json:"start_inclusive"`
				EndExclusive   string `json:"end_exclusive"`
			} `json:"temporal_interval"`
		} `json:"logs"`
	} `json:"operators"`
}

func FetchApple(client *http.Client) (*AppleLogList, error) {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := client.Get("https://valid.apple.com/ct/log_list/current.json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("apple log list: status %d", resp.StatusCode)
	}
	var out AppleLogList
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RFC6962Log represents a discovered usable/readonly log for tailing
type RFC6962Log struct {
	Operator    string
	Description string
	URL         string
	Key         string
	LogID       string
}

func UsableRFC6962Logs(chrome *ChromeLogList) []RFC6962Log {
	var out []RFC6962Log
	if chrome == nil {
		return out
	}
	for _, op := range chrome.Operators {
		for _, l := range op.Logs {
			st := l.State.Usable
			// state.usable can be "usable" or "usable_in", etc. Check if state indicates not retired/rejected
			// Also consider readonly logs as still queryable
			if l.State.Retired != nil {
				continue
			}
			if st == "pending" || st == "qualified" || st == "rejected" {
				continue
			}
			// usable or readonly
			isUsable := st == "usable" || l.State.ReadOnly != nil
			if !isUsable {
				continue
			}
			out = append(out, RFC6962Log{
				Operator:    op.Name,
				Description: l.Description,
				URL:         l.URL,
				Key:         l.Key,
				LogID:       l.LogID,
			})
		}
	}
	return out
}

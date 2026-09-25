package bitbucket

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/ktrysmt/go-bitbucket"

	"github.com/turbot/steampipe-plugin-sdk/v5/plugin"
)

//// Constants
const (
	ColumnDescriptionTitle = "Title of the resource."
)

//// HELPER FUNCTIONS

func connect(_ context.Context, d *plugin.QueryData) *bitbucket.Client {
	username := os.Getenv("BITBUCKET_USERNAME")
	password := os.Getenv("BITBUCKET_PASSWORD")
	token := os.Getenv("BITBUCKET_TOKEN")
	baseurl := os.Getenv("BITBUCKET_API_BASE_URL")

	// Get connection config for plugin
	bitbucketConfig := GetConfig(d.Connection)
	if bitbucketConfig.Token != nil {
		token = *bitbucketConfig.Token
	}
	if bitbucketConfig.Username != nil {
		username = *bitbucketConfig.Username
	}
	if bitbucketConfig.Password != nil {
		password = *bitbucketConfig.Password
	}
	if bitbucketConfig.BaseUrl != nil {
		baseurl = *bitbucketConfig.BaseUrl
	}

	var client *bitbucket.Client
	if token != "" {
		client = bitbucket.NewOAuthbearerToken(token)
	} else {
		if username == "" {
			panic("'username' must be set in the connection configuration if 'token' is not set. Edit your connection configuration file and then restart Steampipe")
		}
		if password == "" {
			panic("'password' must be set in the connection configuration if 'token' is not set. Edit your connection configuration file and then restart Steampipe")
		}
		client = bitbucket.NewBasicAuth(username, password)
	}

	// For private bitbucket setup
	if baseurl != "" {
		client.SetApiBaseURL(baseurl)
	}
	return client
}

func parseRepoFullName(fullName string) (string, string) {
	owner := ""
	repo := ""
	s := strings.Split(fullName, "/")
	owner = s[0]
	if len(s) > 1 {
		repo = s[1]
	}
	return owner, repo
}

// decode API raw response
func decodeResponse(resp *http.Response, v interface{}) error {
	err := json.NewDecoder(resp.Body).Decode(v)
	if err != nil {
		return err
	}
	return nil
}

// decodeJson(apiResponse, responseStruct):: converts raw apiResponse to required output struct
func decodeJson(response interface{}, respObject interface{}) error {
	resp, err := json.Marshal(response)
	if err != nil {
		return err
	}

	err = json.Unmarshal(resp, respObject)
	if err != nil {
		return err
	}
	return nil
}

// resource is not found error handling predicate
func isNotFoundError(err error) bool {
	return strings.Contains(err.Error(), "404")
}

// User don't have required access to all the api on resource
func isForbiddenError(err error) bool {
	return strings.Contains(err.Error(), "403")
}

type ListResponse struct {
	Page     int    `json:"page,omitempty"`
	Pagelen  int    `json:"pagelen,omitempty"`
	MaxDepth int    `json:"maxDepth,omitempty"`
	Size     int    `json:"size,omitempty"`
	Next     string `json:"next,omitempty"`
	Previous string `json:"previous,omitempty"`
}

// makeBitbucketRequest builds and executes an authenticated HTTP request.
func makeBitbucketRequest(ctx context.Context, d *plugin.QueryData, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	cfg := GetConfig(d.Connection)
	if cfg.Token != nil && *cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+*cfg.Token)
	} else if cfg.Username != nil && cfg.Password != nil {
		raw := *cfg.Username + ":" + *cfg.Password
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(raw)))
	}
	req.Header.Set("Accept", "application/json")

	client := connect(ctx, d)
	resp, err := client.HttpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		return resp, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return resp, nil
}


package bitbucket

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/turbot/steampipe-plugin-sdk/v5/grpc/proto"
	"github.com/turbot/steampipe-plugin-sdk/v5/plugin"
	"github.com/turbot/steampipe-plugin-sdk/v5/plugin/transform"
)

// WorkspaceRow is our unified workspace struct returned to Steampipe.
type WorkspaceRow struct {
	Name          string
	Slug          string
	UUID          string
	Is_Private    bool
	Type          string
	WorkspaceType string // "USER_SPECIFIC" or "GLOBAL"
	CreatedOn     interface{}
	UpdatedOn     interface{}
}

func tableBitbucketMyWorkspace(_ context.Context) *plugin.Table {
	return &plugin.Table{
		Name:        "bitbucket_my_workspace",
		Description: "Workspace is where you will create repositories, collaborate on your code, and organize different streams of work in your Bitbucket Cloud account.",
		List: &plugin.ListConfig{
			Hydrate: tableBitbucketMyWorkspaceList,
		},
		Columns: []*plugin.Column{
			{
				Name:        "name",
				Description: "The name of the workspace.",
				Type:        proto.ColumnType_STRING,
			},
			{
				Name:        "slug",
				Description: "The short label that identifies this workspace.",
				Type:        proto.ColumnType_STRING,
			},
			{
				Name:        "uuid",
				Description: "The workspace's immutable id.",
				Type:        proto.ColumnType_STRING,
				Transform:   transform.FromGo(),
			},
			{
				Name:        "is_private",
				Description: "Indicates whether the workspace is publicly accessible, or whether it is private to the members.",
				Type:        proto.ColumnType_BOOL,
				Transform:   transform.FromField("Is_Private"),
			},
			{
				Name:        "type",
				Description: "Type of the Bitbucket resource.",
				Type:        proto.ColumnType_STRING,
			},
			{
				Name:        "workspace_type",
				Description: "USER_SPECIFIC (found via /user/workspaces) or GLOBAL (found via /workspaces).",
				Type:        proto.ColumnType_STRING,
			},
			{
				Name:        "created_on",
				Description: "Timestamp when workspace was created.",
				Type:        proto.ColumnType_TIMESTAMP,
			},
			{
				Name:        "updated_on",
				Description: "Timestamp when workspace was updated.",
				Type:        proto.ColumnType_TIMESTAMP,
			},
			// Standard columns
			{
				Name:        "title",
				Description: ColumnDescriptionTitle,
				Type:        proto.ColumnType_STRING,
				Transform:   transform.FromField("Name"),
			},
		},
	}
}

// tableBitbucketMyWorkspaceList fetches workspaces from BOTH Bitbucket endpoints:
//  1. /user/workspaces  -> works with API Tokens (user-scoped)  -> labeled USER_SPECIFIC
//  2. /workspaces       -> works with admin credentials          -> labeled GLOBAL
//
// 404/403 from either endpoint is silently ignored so both token types work.
func tableBitbucketMyWorkspaceList(ctx context.Context, d *plugin.QueryData, _ *plugin.HydrateData) (interface{}, error) {
	plugin.Logger(ctx).Trace("tableBitbucketMyWorkspaceList")

	cfg := GetConfig(d.Connection)

	baseURL := "https://api.bitbucket.org/2.0"
	if cfg.BaseUrl != nil && *cfg.BaseUrl != "" {
		baseURL = strings.TrimRight(*cfg.BaseUrl, "/")
	}

	// Build auth header: prefer API Token (Bearer), fall back to Basic Auth
	authHeader := ""
	if cfg.Token != nil && *cfg.Token != "" {
		authHeader = "Bearer " + *cfg.Token
	} else if cfg.Username != nil && cfg.Password != nil {
		raw := *cfg.Username + ":" + *cfg.Password
		authHeader = "Basic " + base64.StdEncoding.EncodeToString([]byte(raw))
	}

	seen := map[string]bool{}

	if len(cfg.Workspaces) > 0 {
		var explicitErr error
		for _, slug := range cfg.Workspaces {
			url := baseURL + "/workspaces/" + slug
			ws, err := fetchSingleWorkspace(ctx, url, authHeader)
			if err != nil {
				plugin.Logger(ctx).Warn("tableBitbucketMyWorkspaceList: fetch explicit workspace error", "slug", slug, "err", err)
				explicitErr = err
				continue
			}
			ws.WorkspaceType = "EXPLICIT"
			seen[ws.Slug] = true
			d.StreamListItem(ctx, *ws)
			if d.RowsRemaining(ctx) == 0 {
				return nil, nil
			}
		}
		if len(seen) == 0 && explicitErr != nil {
			return nil, fmt.Errorf("failed to fetch explicit workspaces: %v", explicitErr)
		}
		return nil, nil
	}

	// ── 1. User workspaces (/user/workspaces) — works for API Tokens ──────
	userWS, errUser := fetchWorkspaces(ctx, baseURL+"/user/workspaces?pagelen=100", authHeader)
	if errUser != nil {
		plugin.Logger(ctx).Warn("tableBitbucketMyWorkspaceList: /user/workspaces error", "err", errUser)
	}
	for _, ws := range userWS {
		if seen[ws.Slug] {
			continue
		}
		seen[ws.Slug] = true
		ws.WorkspaceType = "USER_SPECIFIC"
		d.StreamListItem(ctx, ws)
		if d.RowsRemaining(ctx) == 0 {
			return nil, nil
		}
	}

	// ── 2. Global workspaces (/workspaces) — works for admin credentials ──
	globalWS, errGlobal := fetchWorkspaces(ctx, baseURL+"/workspaces?pagelen=100", authHeader)
	if errGlobal != nil {
		plugin.Logger(ctx).Warn("tableBitbucketMyWorkspaceList: /workspaces error", "err", errGlobal)
	}
	for _, ws := range globalWS {
		if seen[ws.Slug] {
			continue // already emitted above
		}
		seen[ws.Slug] = true
		ws.WorkspaceType = "GLOBAL"
		d.StreamListItem(ctx, ws)
		if d.RowsRemaining(ctx) == 0 {
			return nil, nil
		}
	}

	// If BOTH endpoints failed and we got 0 workspaces, return the errors so it doesn't fail silently.
	if len(userWS) == 0 && len(globalWS) == 0 {
		if errUser != nil && errGlobal != nil {
			return nil, fmt.Errorf("failed to fetch workspaces: user endpoint error: %v | global endpoint error: %v", errUser, errGlobal)
		} else if errUser != nil {
			return nil, fmt.Errorf("failed to fetch user workspaces: %v", errUser)
		} else if errGlobal != nil {
			return nil, fmt.Errorf("failed to fetch global workspaces: %v", errGlobal)
		}
	}

	return nil, nil
}

// fetchWorkspaces calls a single Bitbucket workspace listing URL and returns the rows.
// 401/403/404 are returned as errors; other non-200 are also errors.
func fetchWorkspaces(ctx context.Context, url, authHeader string) ([]WorkspaceRow, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("building request for %s: %w", url, err)
	}
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing request for %s: %w", url, err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == 401 || resp.StatusCode == 403 || resp.StatusCode == 404 {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("unexpected HTTP %d from %s: %s", resp.StatusCode, url, string(body))
	}

	// /user/workspaces returns: { "values": [ { "workspace": { name, slug, uuid, type }, "is_private": ... } ] }
	// /workspaces returns:      { "values": [ { "name": ..., "slug": ..., "uuid": ..., "type": ... } ] }
	var result struct {
		Values []struct {
			Name      string      `json:"name"`
			Slug      string      `json:"slug"`
			UUID      string      `json:"uuid"`
			IsPrivate bool        `json:"is_private"`
			Type      string      `json:"type"`
			CreatedOn interface{} `json:"created_on"`
			UpdatedOn interface{} `json:"updated_on"`
			Workspace *struct {
				Name      string      `json:"name"`
				Slug      string      `json:"slug"`
				UUID      string      `json:"uuid"`
				Type      string      `json:"type"`
				CreatedOn interface{} `json:"created_on"`
				UpdatedOn interface{} `json:"updated_on"`
			} `json:"workspace"`
		} `json:"values"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parsing response from %s: %w", url, err)
	}

	var rows []WorkspaceRow
	for _, v := range result.Values {
		if v.Workspace != nil {
			// Nested structure from /user/workspaces
			rows = append(rows, WorkspaceRow{
				Name:       v.Workspace.Name,
				Slug:       v.Workspace.Slug,
				UUID:       v.Workspace.UUID,
				Is_Private: v.IsPrivate,
				Type:       v.Workspace.Type,
				CreatedOn:  v.Workspace.CreatedOn,
				UpdatedOn:  v.Workspace.UpdatedOn,
			})
		} else {
			// Flat structure from /workspaces
			rows = append(rows, WorkspaceRow{
				Name:       v.Name,
				Slug:       v.Slug,
				UUID:       v.UUID,
				Is_Private: v.IsPrivate,
				Type:       v.Type,
				CreatedOn:  v.CreatedOn,
				UpdatedOn:  v.UpdatedOn,
			})
		}
	}
	return rows, nil
}

func fetchSingleWorkspace(ctx context.Context, url, authHeader string) (*WorkspaceRow, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("building request for %s: %w", url, err)
	}
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing request for %s: %w", url, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body from %s: %w", url, err)
	}
	if resp.StatusCode == 401 || resp.StatusCode == 403 || resp.StatusCode == 404 {
		return nil, fmt.Errorf("http %d from %s", resp.StatusCode, url)
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("unexpected http %d from %s: %s", resp.StatusCode, url, string(body))
	}

	var v struct {
		Name      string      `json:"name"`
		Slug      string      `json:"slug"`
		UUID      string      `json:"uuid"`
		IsPrivate bool        `json:"is_private"`
		Type      string      `json:"type"`
		CreatedOn interface{} `json:"created_on"`
		UpdatedOn interface{} `json:"updated_on"`
	}

	if err := json.Unmarshal(body, &v); err != nil {
		return nil, fmt.Errorf("parsing response from %s: %w", url, err)
	}

	return &WorkspaceRow{
		Name:       v.Name,
		Slug:       v.Slug,
		UUID:       v.UUID,
		Is_Private: v.IsPrivate,
		Type:       v.Type,
		CreatedOn:  v.CreatedOn,
		UpdatedOn:  v.UpdatedOn,
	}, nil
}

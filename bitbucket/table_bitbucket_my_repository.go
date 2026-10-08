package bitbucket

import (
	"context"
	"fmt"
	"strings"

	"github.com/ktrysmt/go-bitbucket"
	"github.com/turbot/steampipe-plugin-sdk/v5/plugin"
)

func tableBitbucketMyRepository(_ context.Context) *plugin.Table {
	return &plugin.Table{
		Name:        "bitbucket_my_repository",
		Description: "BitBucket repositories that you are associated with. BitBucket Repositories contain all of your project's files and each file's revision history.",
		List: &plugin.ListConfig{
			ParentHydrate: tableBitbucketMyWorkspaceList,
			Hydrate:       tableBitbucketMyRepositoryList,
		},
		Columns: bitBucketRepositoryColumns(),
	}
}

func tableBitbucketMyRepositoryList(ctx context.Context, d *plugin.QueryData, h *plugin.HydrateData) (interface{}, error) {
	owner := h.Item.(WorkspaceRow).Slug
	cfg := GetConfig(d.Connection)

	baseURL := "https://api.bitbucket.org/2.0"
	if cfg.BaseUrl != nil && *cfg.BaseUrl != "" {
		baseURL = strings.TrimRight(*cfg.BaseUrl, "/")
	}
	url := fmt.Sprintf("%s/repositories/%s?pagelen=100", baseURL, owner)

	for url != "" {
		nextURL, err := func(currentURL string) (string, error) {
			resp, err := makeBitbucketRequest(ctx, d, currentURL)
			if err != nil {
				return "", err
			}
			defer func() {
				_ = resp.Body.Close()
			}()

			var result struct {
				Values []bitbucket.Repository `json:"values"`
				Next   string                 `json:"next"`
			}

			if err := decodeResponse(resp, &result); err != nil {
				return "", err
			}

			for _, repo := range result.Values {
				d.StreamListItem(ctx, repo)
				if d.RowsRemaining(ctx) == 0 {
					return "", nil
				}
			}

			return result.Next, nil
		}(url)

		if err != nil {
			return nil, err
		}
		if nextURL == "" && d.RowsRemaining(ctx) == 0 {
			return nil, nil
		}
		url = nextURL
	}

	return nil, nil
}

func tableBitbucketDefaultReviewersList(ctx context.Context, d *plugin.QueryData, h *plugin.HydrateData) (interface{}, error) {
	plugin.Logger(ctx).Debug("tableBitbucketDefaultReviewersList")
	data := h.Item.(bitbucket.Repository)
	owner:= data.Owner["display_name"]
	uuid:= data.Uuid
	repoSlug:= data.Slug
	client := connect(ctx, d)

	opts := &bitbucket.RepositoryOptions{
		Owner: owner.(string),
		Uuid:  uuid,
		RepoSlug: repoSlug,
	}

	response, err := client.Repositories.Repository.ListDefaultReviewers(opts)
	if err != nil {
		if isNotFoundError(err) {
			return nil, nil
		}
		plugin.Logger(ctx).Error("tableBitbucketDefaultReviewersList", "Error", err)
		return nil, err
	}

	if response == nil {
		return nil, nil
	}
	return response.DefaultReviewers,nil
}
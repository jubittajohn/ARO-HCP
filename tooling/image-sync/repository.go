package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/containers/azcontainerregistry"
)

// Registry is the interface for accessing image repositories
type Registry interface {
	GetTags(context.Context, string) ([]string, error)
}

// AuthedTransport is a http.RoundTripper that adds an Authorization header
type AuthedTransport struct {
	Key     string
	Wrapped http.RoundTripper
}

// RoundTrip implements http.RoundTripper and sets Authorization header
func (t *AuthedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", t.Key)
	return t.Wrapped.RoundTrip(req)
}

// QuayRegistry implements Quay Repository access
type QuayRegistry struct {
	httpclient   *http.Client
	baseUrl      string
	numberOftags int
}

// NewQuayRegistry creates a new QuayRegistry access client
func NewQuayRegistry(cfg *SyncConfig, bearerToken string) *QuayRegistry {
	return &QuayRegistry{
		httpclient: &http.Client{Timeout: time.Duration(cfg.RequestTimeout) * time.Second,
			Transport: &AuthedTransport{
				Key:     "Bearer " + bearerToken,
				Wrapped: http.DefaultTransport,
			},
		},
		baseUrl:      "https://quay.io",
		numberOftags: cfg.NumberOfTags,
	}
}

type TagsResponse struct {
	Tags          []Tags
	Page          int
	HasAdditional bool
}

type Tags struct {
	Name string
}

// GetTags returns the tags for the given image
func (q *QuayRegistry) GetTags(ctx context.Context, image string) ([]string, error) {
	// Todo pagination
	Log().Debugw("Getting tags for image", "image", image)
	path := fmt.Sprintf("%s/api/v1/repository/%s/tag/", q.baseUrl, image)
	req, err := http.NewRequestWithContext(ctx, "GET", path, nil)

	Log().Debugw("Sending request", "path", path)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %v", err)
	}
	resp, err := q.httpclient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %v", err)
	}
	Log().Debugw("Got response", "statuscode", resp.StatusCode)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %v", err)
	}

	var tagsResponse TagsResponse
	err = json.Unmarshal(body, &tagsResponse)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %v", err)
	}

	var tags []string

	for _, tag := range tagsResponse.Tags {
		if tag.Name == "latest" {
			continue
		}
		tags = append(tags, tag.Name)
		if len(tags) >= q.numberOftags {
			break
		}
	}

	return tags, nil
}

type getAccessToken func(context.Context, *azidentity.DefaultAzureCredential) (string, error)
type getACRUrl func(string) string

// AzureContainerRegistry implements ACR Repository access
type AzureContainerRegistry struct {
	acrName      string
	credential   *azidentity.DefaultAzureCredential
	acrClient    *azcontainerregistry.Client
	httpClient   *http.Client
	numberOfTags int
	tenantId     string

	getAccessTokenImpl getAccessToken
	getACRUrlImpl      getACRUrl
}

// NewAzureContainerRegistry creates a new AzureContainerRegistry access client
func NewAzureContainerRegistry(cfg *SyncConfig) *AzureContainerRegistry {
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		Log().Fatalf("failed to obtain a credential: %v", err)
	}

	client, err := azcontainerregistry.NewClient(fmt.Sprintf("https://%s", cfg.AcrRegistry), cred, nil)
	if err != nil {
		Log().Fatalf("failed to create client: %v", err)
	}

	return &AzureContainerRegistry{
		acrName:      cfg.AcrRegistry,
		acrClient:    client,
		credential:   cred,
		httpClient:   &http.Client{Timeout: time.Duration(cfg.RequestTimeout) * time.Second},
		numberOfTags: cfg.NumberOfTags,
		tenantId:     cfg.TenantId,

		getAccessTokenImpl: func(ctx context.Context, dac *azidentity.DefaultAzureCredential) (string, error) {
			accessToken, err := dac.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{"https://management.core.windows.net//.default"}})
			if err != nil {
				return "", err
			}
			return accessToken.Token, nil
		},

		getACRUrlImpl: func(acrName string) string {
			return fmt.Sprintf("https://%s", acrName)
		},
	}
}

type AuthSecret struct {
	RefreshToken string `json:"refresh_token"`
}

func (a *AzureContainerRegistry) createOauthRequest(ctx context.Context, accessToken string) (*http.Request, error) {
	path := fmt.Sprintf("%s/oauth2/exchange/", a.getACRUrlImpl(a.acrName))

	form := url.Values{}
	form.Add("grant_type", "access_token")
	form.Add("service", a.acrName)
	form.Add("tenant", a.tenantId)
	form.Add("access_token", accessToken)

	Log().Debugw("Creating request", "path", path)
	req, err := http.NewRequestWithContext(ctx, "POST", path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %v", err)
	}
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
	return req, nil
}

func (a *AzureContainerRegistry) GetPullSecret(ctx context.Context) (*AuthSecret, error) {
	accessToken, err := a.getAccessTokenImpl(ctx, a.credential)
	if err != nil {
		return nil, fmt.Errorf("failed to get access token: %v", err)
	}

	req, err := a.createOauthRequest(ctx, accessToken)
	if err != nil {
		return nil, fmt.Errorf("failed to create OAuth request: %v", err)
	}

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %v", err)
	}

	var authSecret AuthSecret

	err = json.Unmarshal(body, &authSecret)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %v", err)
	}

	return &authSecret, nil
}

// EnsureRepositoryExists ensures that the repository exists
func (a *AzureContainerRegistry) RepositoryExists(ctx context.Context, repository string) (bool, error) {

	pager := a.acrClient.NewListRepositoriesPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return false, fmt.Errorf("failed to advance page: %v", err)
		}
		for _, v := range page.Repositories.Names {
			if *v == repository {
				return true, nil
			}
		}
	}

	return false, nil
}

func ptr[T any](v T) *T {
	return &v
}

// GetTags returns the tags in the given repository
func (a *AzureContainerRegistry) GetTags(ctx context.Context, repository string) ([]string, error) {

	var tags []string

	pager := a.acrClient.NewListTagsPager(repository, &azcontainerregistry.ClientListTagsOptions{OrderBy: ptr(azcontainerregistry.ArtifactTagOrderByLastUpdatedOnDescending)})
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to advance page: %v", err)
		}
		for _, v := range page.Tags {
			if *v.Name == "latest" {
				continue
			}
			tags = append(tags, *v.Name)
		}
		if len(tags) >= a.numberOfTags {
			break
		}
	}

	return tags, nil
}

// OCIRegistry implements OCI Repository access
type OCIRegistry struct {
	httpclient   *http.Client
	baseURL      string
	numberOftags int
}

// NewOCIRegistry creates a new OCIRegistry access client
func NewOCIRegistry(cfg *SyncConfig, baseURL string) *OCIRegistry {
	o := &OCIRegistry{
		httpclient:   &http.Client{Timeout: time.Duration(cfg.RequestTimeout) * time.Second},
		numberOftags: cfg.NumberOfTags,
	}
	if !strings.HasPrefix(o.baseURL, "https://") {
		o.baseURL = fmt.Sprintf("https://%s", baseURL)
	} else {
		o.baseURL = baseURL
	}
	return o
}

type rawManifest struct {
	TimeUploadedMs string
	Tag            []string
}

type rawOCIResponse struct {
	Manifest map[string]rawManifest
	Tags     []string
}

func getNewestTags(response *rawOCIResponse, numberOfTags int) ([]string, error) {
	var returnTags []string

	uploadedTagAt := make(map[int][]string)
	uploadTimes := make([]int, 0, len(response.Manifest))

	for _, manifest := range response.Manifest {
		if len(manifest.Tag) == 0 {
			continue
		}
		uploadedAt, err := strconv.Atoi(manifest.TimeUploadedMs)
		if err != nil {
			return nil, fmt.Errorf("failed to parse manifest %s time: %v", manifest, err)
		}
		uploadedTagAt[uploadedAt] = manifest.Tag
		uploadTimes = append(uploadTimes, uploadedAt)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(uploadTimes)))

	for i, k := range uploadTimes {
		if i >= numberOfTags {
			break
		}
		returnTags = append(returnTags, uploadedTagAt[k]...)
	}

	return returnTags, nil
}

// GetTags returns the tags in the given repository
func (o *OCIRegistry) GetTags(ctx context.Context, image string) ([]string, error) {
	Log().Debugw("Getting tags for image", "image", image)

	path := fmt.Sprintf("%s/v2/%s/tags/list", o.baseURL, image)
	req, err := http.NewRequestWithContext(ctx, "GET", path, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %v", err)
	}

	Log().Debugw("Sending request", "path", path)
	resp, err := o.httpclient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %v", err)
	}
	Log().Debugw("Got response", "statuscode", resp.StatusCode)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %v", err)
	}

	var rawOCIResponse rawOCIResponse
	err = json.Unmarshal(body, &rawOCIResponse)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %v", err)
	}

	return getNewestTags(&rawOCIResponse, o.numberOftags)
}

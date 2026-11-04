package jira

import (
	"context"
	"net/http"
)

// ServerInfo is GET /rest/api/3/serverInfo's answer, the parts read.
type ServerInfo struct {
	BaseURL        string `json:"baseUrl"`
	Version        string `json:"version"`
	DeploymentType string `json:"deploymentType"`
	ServerTitle    string `json:"serverTitle"`
}

// ServerInfo reads the site's identity: its base URL and deployment type
// ("Cloud").
func (c *Client) ServerInfo(ctx context.Context) (ServerInfo, error) {
	var si ServerInfo
	if err := c.do(ctx, "server info", http.MethodGet, pathServerInfo, nil, nil, &si); err != nil {
		return ServerInfo{}, err
	}
	return si, nil
}

// Myself is the account the credentials authenticate as.
type Myself struct {
	AccountID   string `json:"accountId"`
	DisplayName string `json:"displayName"`
}

// Myself reads GET /rest/api/3/myself: the cheapest call that proves the
// credentials.
func (c *Client) Myself(ctx context.Context) (Myself, error) {
	var m Myself
	if err := c.do(ctx, "myself", http.MethodGet, pathMyself, nil, nil, &m); err != nil {
		return Myself{}, err
	}
	return m, nil
}

// Project is one Jira project's identity. ID is what a binding stores: it
// survives a rename of the key.
type Project struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
}

// Project reads GET /rest/api/3/project/{key}; an unknown key — or one the
// credentials may not browse — is not_found.
func (c *Client) Project(ctx context.Context, key string) (Project, error) {
	var p Project
	if err := c.do(ctx, "project", http.MethodGet, pathProject(key), nil, nil, &p); err != nil {
		return Project{}, err
	}
	return p, nil
}

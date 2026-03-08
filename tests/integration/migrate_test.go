// Copyright 2021 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	auth_model "code.gitea.io/gitea/models/auth"
	issues_model "code.gitea.io/gitea/models/issues"
	repo_model "code.gitea.io/gitea/models/repo"
	"code.gitea.io/gitea/models/unittest"
	user_model "code.gitea.io/gitea/models/user"
	"code.gitea.io/gitea/modules/setting"
	"code.gitea.io/gitea/modules/structs"
	"code.gitea.io/gitea/services/migrations"

	"github.com/stretchr/testify/assert"
)

func TestMigrateLocalPath(t *testing.T) {
	assert.NoError(t, unittest.PrepareTestDatabase())

	adminUser := unittest.AssertExistsAndLoadBean(t, &user_model.User{Name: "user1"})

	old := setting.ImportLocalPaths
	setting.ImportLocalPaths = true

	basePath := t.TempDir()

	lowercasePath := filepath.Join(basePath, "lowercase")
	err := os.Mkdir(lowercasePath, 0o700)
	assert.NoError(t, err)

	err = migrations.IsMigrateURLAllowed(lowercasePath, adminUser)
	assert.NoError(t, err, "case lowercase path")

	mixedcasePath := filepath.Join(basePath, "mIxeDCaSe")
	err = os.Mkdir(mixedcasePath, 0o700)
	assert.NoError(t, err)

	err = migrations.IsMigrateURLAllowed(mixedcasePath, adminUser)
	assert.NoError(t, err, "case mixedcase path")

	setting.ImportLocalPaths = old
}

func TestMigrateGiteaForm(t *testing.T) {
	onGiteaRun(t, func(t *testing.T, u *url.URL) {
		AllowLocalNetworks := setting.Migrations.AllowLocalNetworks
		setting.Migrations.AllowLocalNetworks = true
		AppVer := setting.AppVer
		// Gitea SDK (go-sdk) need to parse the AppVer from server response, so we must set it to a valid version string.
		setting.AppVer = "1.16.0"
		defer func() {
			setting.Migrations.AllowLocalNetworks = AllowLocalNetworks
			setting.AppVer = AppVer
			migrations.Init()
		}()
		assert.NoError(t, migrations.Init())

		ownerName := "user2"
		repoName := "repo1"
		repoOwner := unittest.AssertExistsAndLoadBean(t, &user_model.User{Name: ownerName})
		session := loginUser(t, ownerName)
		token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteRepository, auth_model.AccessTokenScopeReadMisc)

		// Step 0: verify the repo is available
		req := NewRequestf(t, "GET", "/%s/%s", ownerName, repoName)
		_ = session.MakeRequest(t, req, http.StatusOK)
		// Step 1: get the Gitea migration form
		req = NewRequestf(t, "GET", "/repo/migrate/?service_type=%d", structs.GiteaService)
		resp := session.MakeRequest(t, req, http.StatusOK)
		// Step 2: load the form
		htmlDoc := NewHTMLParser(t, resp.Body)
		form := htmlDoc.doc.Find(`form.ui.form[action^="/repo/migrate"]`)
		link, exists := form.Attr("action")
		assert.True(t, exists, "The template has changed")
		serviceInput, exists := form.Find(`input[name="service"]`).Attr("value")
		assert.True(t, exists)
		assert.Equal(t, fmt.Sprintf("%d", structs.GiteaService), serviceInput)
		// Step 4: submit the migration to only migrate issues
		migratedRepoName := "otherrepo"
		req = NewRequestWithValues(t, "POST", link, map[string]string{
			"service":     fmt.Sprintf("%d", structs.GiteaService),
			"clone_addr":  fmt.Sprintf("%s%s/%s", u, ownerName, repoName),
			"auth_token":  token,
			"issues":      "on",
			"repo_name":   migratedRepoName,
			"description": "",
			"uid":         strconv.FormatInt(repoOwner.ID, 10),
		})
		resp = session.MakeRequest(t, req, http.StatusSeeOther)
		// Step 5: a redirection displays the migrated repository
		loc := resp.Header().Get("Location")
		assert.Equal(t, fmt.Sprintf("/%s/%s", ownerName, migratedRepoName), loc)
		// Step 6: check the repo was created
		unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{Name: migratedRepoName})
	})
}

func Test_UpdateCommentsMigrationsByType(t *testing.T) {
	assert.NoError(t, unittest.PrepareTestDatabase())

	err := issues_model.UpdateCommentsMigrationsByType(t.Context(), structs.GithubService, "1", 1)
	assert.NoError(t, err)
}

func Test_MigrateFromGiteaToGitea(t *testing.T) {
	onGiteaRun(t, func(t *testing.T, u *url.URL) {
		AllowLocalNetworks := setting.Migrations.AllowLocalNetworks
		setting.Migrations.AllowLocalNetworks = true
		AppVer := setting.AppVer
		setting.AppVer = "1.16.0"
		defer func() {
			setting.Migrations.AllowLocalNetworks = AllowLocalNetworks
			setting.AppVer = AppVer
			migrations.Init()
		}()
		assert.NoError(t, migrations.Init())

		ownerName := "user2"
		repoName := "repo1"
		owner := unittest.AssertExistsAndLoadBean(t, &user_model.User{Name: ownerName})
		session := loginUser(t, ownerName)
		token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteRepository, auth_model.AccessTokenScopeReadMisc)

		migratedRepoName := "migrated-gitea-repo"
		cloneAddr := fmt.Sprintf("%s%s/%s", u, ownerName, repoName)

		req := NewRequestWithJSON(t, "POST", "/api/v1/repos/migrate", &structs.MigrateRepoOptions{
			CloneAddr:    cloneAddr,
			AuthToken:    token,
			RepoOwnerID:  owner.ID,
			RepoName:     migratedRepoName,
			Service:      structs.GiteaService.Name(),
			Wiki:         true,
			Milestones:   true,
			Labels:       true,
			Issues:       true,
			PullRequests: true,
			Releases:     true,
		}).AddTokenAuth(token)
		MakeRequest(t, req, http.StatusCreated)

		migratedRepo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{Name: migratedRepoName})
		assert.Equal(t, owner.ID, migratedRepo.OwnerID)
		assert.Equal(t, structs.GiteaService, migratedRepo.OriginalServiceType)
		assert.Equal(t, cloneAddr, migratedRepo.OriginalURL)

		issueCount := unittest.GetCount(t,
			&issues_model.Issue{RepoID: migratedRepo.ID},
			unittest.Cond("is_pull = ?", false),
		)
		assert.Greater(t, issueCount, 0)

		pullCount := unittest.GetCount(t,
			&issues_model.Issue{RepoID: migratedRepo.ID},
			unittest.Cond("is_pull = ?", true),
		)
		assert.Greater(t, pullCount, 0)
	})
}

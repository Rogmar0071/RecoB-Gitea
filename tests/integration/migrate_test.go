// Copyright 2021 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"fmt"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	auth_model "code.gitea.io/gitea/models/auth"
	"code.gitea.io/gitea/models/db"
	git_model "code.gitea.io/gitea/models/git"
	issues_model "code.gitea.io/gitea/models/issues"
	repo_model "code.gitea.io/gitea/models/repo"
	"code.gitea.io/gitea/models/unittest"
	user_model "code.gitea.io/gitea/models/user"
	"code.gitea.io/gitea/modules/gitrepo"
	"code.gitea.io/gitea/modules/setting"
	"code.gitea.io/gitea/modules/structs"
	"code.gitea.io/gitea/services/migrations"
	"code.gitea.io/gitea/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func setupGiteaMockServer(t *testing.T) *httptest.Server {
	t.Helper()

	// Create a bare git repo with the expected branches and tags
	repoDir := t.TempDir()
	cmds := [][]string{
		{"git", "init", "--bare", repoDir},
	}
	for _, cmd := range cmds {
		out, err := exec.Command(cmd[0], cmd[1:]...).CombinedOutput()
		require.NoError(t, err, "command %v failed: %s", cmd, out)
	}

	// Create a work tree to populate the bare repo
	workDir := t.TempDir()
	for _, cmd := range [][]string{
		{"git", "clone", repoDir, workDir},
		{"git", "-C", workDir, "checkout", "-b", "master"},
		{"git", "-C", workDir, "commit", "--allow-empty", "-m", "initial"},
		{"git", "-C", workDir, "tag", "V1"},
		{"git", "-C", workDir, "commit", "--allow-empty", "-m", "second"},
		{"git", "-C", workDir, "tag", "v2-rc1"},
		{"git", "-C", workDir, "checkout", "-b", "6543-patch-1"},
		{"git", "-C", workDir, "commit", "--allow-empty", "-m", "patch"},
		{"git", "-C", workDir, "checkout", "-b", "add-xkcd-2199", "master"},
		{"git", "-C", workDir, "commit", "--allow-empty", "-m", "xkcd"},
		{"git", "-C", workDir, "push", "origin", "master", "6543-patch-1", "add-xkcd-2199", "V1", "v2-rc1"},
	} {
		out, err := exec.Command(cmd[0], cmd[1:]...).CombinedOutput()
		require.NoError(t, err, "command %v failed: %s", cmd, out)
	}
	// Get the SHA of the add-xkcd-2199 branch for the PR head
	headSHABytes, err := exec.Command("git", "-C", workDir, "rev-parse", "add-xkcd-2199").Output()
	require.NoError(t, err)
	headSHA := strings.TrimSpace(string(headSHABytes))
	masterSHABytes, err := exec.Command("git", "-C", workDir, "rev-parse", "master").Output()
	require.NoError(t, err)
	masterSHA := strings.TrimSpace(string(masterSHABytes))
	patchSHABytes, err := exec.Command("git", "-C", workDir, "rev-parse", "6543-patch-1").Output()
	require.NoError(t, err)
	patchSHA := strings.TrimSpace(string(patchSHABytes))

	// Find git-http-backend
	execPathBytes, err := exec.Command("git", "--exec-path").Output()
	require.NoError(t, err)
	httpBackend := filepath.Join(strings.TrimSpace(string(execPathBytes)), "git-http-backend")

	mux := http.NewServeMux()

	// Serve git HTTP protocol
	mux.HandleFunc("/gitea/test_repo.git/", func(w http.ResponseWriter, r *http.Request) {
		// Strip the repo path prefix for git-http-backend
		pathInfo := strings.TrimPrefix(r.URL.Path, "/gitea/test_repo.git")
		handler := &cgi.Handler{
			Path: httpBackend,
			Dir:  repoDir,
			Env: []string{
				"GIT_PROJECT_ROOT=" + filepath.Dir(repoDir),
				"GIT_HTTP_EXPORT_ALL=1",
			},
		}
		// Set PATH_INFO for CGI
		r.URL.Path = "/" + filepath.Base(repoDir) + pathInfo
		handler.ServeHTTP(w, r)
	})

	var serverURL string

	// Helper to write JSON responses
	writeJSON := func(w http.ResponseWriter, data string) {
		w.Header().Set("Content-Type", "application/json")
		// Replace {{SERVER_URL}} placeholder with the actual server URL
		fmt.Fprint(w, strings.ReplaceAll(data, "{{SERVER_URL}}", serverURL))
	}

	user6543 := `{"id":689,"login":"6543","full_name":"","email":"6543@obermui.de","avatar_url":"","username":"6543"}`
	userGhost := `{"id":-1,"login":"Ghost","full_name":"","email":"","avatar_url":"","username":"Ghost"}`
	now := `"2020-01-01T00:00:00Z"`

	// API: server version
	mux.HandleFunc("/api/v1/version", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, `{"version":"1.22.0"}`)
	})

	// API: global settings
	mux.HandleFunc("/api/v1/settings/api", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, `{"max_response_items":50,"default_paging_num":30,"default_git_trees_per_page":40,"default_max_blob_size":10485760}`)
	})

	// API: repo info (exact match, no trailing slash)
	mux.HandleFunc("/api/v1/repos/gitea/test_repo", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, `{
			"id":1,
			"name":"test_repo",
			"full_name":"gitea/test_repo",
			"owner":{"id":1,"login":"gitea","username":"gitea"},
			"private":false,
			"html_url":"{{SERVER_URL}}/gitea/test_repo",
			"clone_url":"{{SERVER_URL}}/gitea/test_repo.git",
			"default_branch":"master",
			"description":"test repo for migration"
		}`)
	})

	// API: topics
	mux.HandleFunc("/api/v1/repos/gitea/test_repo/topics", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, `{"topics":[]}`)
	})

	// API: milestones
	mux.HandleFunc("/api/v1/repos/gitea/test_repo/milestones", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, `[
			{"id":1,"title":"V1","description":"first milestone","state":"closed","created_at":`+now+`,"updated_at":`+now+`,"closed_at":`+now+`},
			{"id":2,"title":"V2 Finalize","description":"second milestone","state":"open","created_at":`+now+`,"updated_at":`+now+`}
		]`)
	})

	// API: labels
	mux.HandleFunc("/api/v1/repos/gitea/test_repo/labels", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, `[
			{"id":1,"name":"Bug","color":"#ee0701","description":""},
			{"id":2,"name":"Question","color":"#d876e3","description":""}
		]`)
	})

	// API: releases
	mux.HandleFunc("/api/v1/repos/gitea/test_repo/releases", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, `[
			{"id":1,"tag_name":"V1","target_commitish":"master","name":"First Release","body":"as title","draft":false,"prerelease":false,"created_at":`+now+`,"published_at":`+now+`,"publisher":`+user6543+`,"assets":[]},
			{"id":2,"tag_name":"v2-rc1","target_commitish":"master","name":"Second Release","body":"this repo has:\n- issues\n- pulls","draft":false,"prerelease":true,"created_at":`+now+`,"published_at":`+now+`,"publisher":`+user6543+`,"assets":[]}
		]`)
	})

	// API: issues
	mux.HandleFunc("/api/v1/repos/gitea/test_repo/issues", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, `[
			{"number":1,"title":"issue1","body":"","state":"open","poster":`+user6543+`,"labels":[],"created_at":`+now+`,"updated_at":`+now+`,"is_locked":false},
			{"number":2,"title":"issue2","body":"","state":"open","poster":`+user6543+`,"labels":[],"created_at":`+now+`,"updated_at":`+now+`,"is_locked":false},
			{"number":3,"title":"issue3","body":"","state":"open","poster":`+user6543+`,"labels":[],"created_at":`+now+`,"updated_at":`+now+`,"is_locked":false},
			{"number":4,"title":"what is this repo about?","body":"","state":"closed","poster":`+userGhost+`,"labels":[{"id":2,"name":"Question","color":"#d876e3"}],"milestone":{"id":1,"title":"V1"},"created_at":`+now+`,"updated_at":`+now+`,"closed_at":`+now+`,"is_locked":true},
			{"number":5,"title":"issue5","body":"","state":"open","poster":`+user6543+`,"labels":[],"created_at":`+now+`,"updated_at":`+now+`,"is_locked":false},
			{"number":6,"title":"issue6","body":"","state":"open","poster":`+user6543+`,"labels":[],"created_at":`+now+`,"updated_at":`+now+`,"is_locked":false},
			{"number":7,"title":"issue7","body":"","state":"open","poster":`+user6543+`,"labels":[],"created_at":`+now+`,"updated_at":`+now+`,"is_locked":false}
		]`)
	})

	// API: issue comments
	mux.HandleFunc("/api/v1/repos/gitea/test_repo/issues/4/comments", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, `[
			{"id":1553,"body":"TESTSET for gitea2gitea migration\n","poster":`+user6543+`,"created_at":`+now+`,"updated_at":`+now+`},
			{"id":1554,"body":"Oh!\n","poster":`+userGhost+`,"created_at":`+now+`,"updated_at":`+now+`}
		]`)
	})
	// Return empty comments for all other issues
	mux.HandleFunc("/api/v1/repos/gitea/test_repo/issues/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/comments") {
			writeJSON(w, `[]`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/reactions") {
			writeJSON(w, `[]`)
			return
		}
		http.NotFound(w, r)
	})

	// API: issue reactions
	mux.HandleFunc("/api/v1/repos/gitea/test_repo/issues/4/reactions", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, `[
			{"user":`+userGhost+`,"reaction":"gitea"},
			{"user":`+user6543+`,"reaction":"laugh"}
		]`)
	})

	// API: comment reactions
	mux.HandleFunc("/api/v1/repos/gitea/test_repo/issues/comments/", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, `[]`)
	})

	// API: pull requests
	mergeCommitSHA := "827aa28a907853e5ddfa40c8f9bc52471a2685fd"
	merged := `"2020-01-01T00:00:00Z"`
	headRepo := `{"name":"test_repo","owner":{"id":689,"login":"6543","username":"6543"},"clone_url":"{{SERVER_URL}}/gitea/test_repo.git"}`
	baseRepo := `{"name":"test_repo","owner":{"id":1,"login":"gitea","username":"gitea"},"clone_url":"{{SERVER_URL}}/gitea/test_repo.git"}`
	// API: PR reviews (prefix match, must be registered before exact /pulls)
	mux.HandleFunc("/api/v1/repos/gitea/test_repo/pulls/", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, `[]`)
	})
	mux.HandleFunc("/api/v1/repos/gitea/test_repo/pulls", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, `[
			{"number":8,"title":"add garbage for close pull","body":"well you'll see","state":"closed","poster":`+user6543+`,"labels":[],"has_merged":false,"created_at":`+now+`,"updated_at":`+now+`,"closed_at":`+now+`,"is_locked":false,"head":{"label":"6543-patch-1","ref":"6543-patch-1","sha":"`+patchSHA+`","repo":`+baseRepo+`},"base":{"label":"master","ref":"master","sha":"`+masterSHA+`","repo":`+baseRepo+`},"patch_url":"{{SERVER_URL}}/gitea/test_repo/pulls/8.patch"},
			{"number":9,"title":"pr9","body":"","state":"open","poster":`+user6543+`,"labels":[],"has_merged":false,"created_at":`+now+`,"updated_at":`+now+`,"is_locked":false,"head":{"label":"6543-patch-1","ref":"6543-patch-1","sha":"`+patchSHA+`","repo":`+baseRepo+`},"base":{"label":"master","ref":"master","sha":"`+masterSHA+`","repo":`+baseRepo+`},"patch_url":"{{SERVER_URL}}/gitea/test_repo/pulls/9.patch"},
			{"number":10,"title":"pr10","body":"","state":"open","poster":`+user6543+`,"labels":[],"has_merged":false,"created_at":`+now+`,"updated_at":`+now+`,"is_locked":false,"head":{"label":"6543-patch-1","ref":"6543-patch-1","sha":"`+patchSHA+`","repo":`+baseRepo+`},"base":{"label":"master","ref":"master","sha":"`+masterSHA+`","repo":`+baseRepo+`},"patch_url":"{{SERVER_URL}}/gitea/test_repo/pulls/10.patch"},
			{"number":11,"title":"pr11","body":"","state":"open","poster":`+user6543+`,"labels":[],"has_merged":false,"created_at":`+now+`,"updated_at":`+now+`,"is_locked":false,"head":{"label":"6543-patch-1","ref":"6543-patch-1","sha":"`+patchSHA+`","repo":`+baseRepo+`},"base":{"label":"master","ref":"master","sha":"`+masterSHA+`","repo":`+baseRepo+`},"patch_url":"{{SERVER_URL}}/gitea/test_repo/pulls/11.patch"},
			{"number":12,"title":"Dont Touch","body":"dont touch","state":"closed","poster":`+user6543+`,"labels":[],"milestone":{"id":2,"title":"V2 Finalize"},"has_merged":true,"merged_commit_sha":"`+mergeCommitSHA+`","merged_at":`+merged+`,"created_at":`+now+`,"updated_at":`+now+`,"closed_at":`+now+`,"is_locked":false,"head":{"label":"6543-patch-1","ref":"6543-patch-1","sha":"`+patchSHA+`","repo":`+baseRepo+`},"base":{"label":"master","ref":"master","sha":"`+masterSHA+`","repo":`+baseRepo+`},"patch_url":"{{SERVER_URL}}/gitea/test_repo/pulls/12.patch"},
			{"number":13,"title":"extend","body":"","state":"open","poster":`+user6543+`,"labels":[],"has_merged":false,"created_at":`+now+`,"updated_at":`+now+`,"is_locked":true,"head":{"label":"add-xkcd-2199","ref":"add-xkcd-2199","sha":"`+headSHA+`","repo":`+headRepo+`},"base":{"label":"master","ref":"master","sha":"`+masterSHA+`","repo":`+baseRepo+`},"patch_url":"{{SERVER_URL}}/gitea/test_repo/pulls/13.patch"}
		]`)
	})

	server := httptest.NewServer(mux)
	serverURL = server.URL
	t.Cleanup(server.Close)
	return server
}

func Test_MigrateFromGiteaToGitea(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	AllowLocalNetworks := setting.Migrations.AllowLocalNetworks
	setting.Migrations.AllowLocalNetworks = true
	defer func() {
		setting.Migrations.AllowLocalNetworks = AllowLocalNetworks
		migrations.Init()
	}()
	require.NoError(t, migrations.Init())

	mockServer := setupGiteaMockServer(t)

	owner := unittest.AssertExistsAndLoadBean(t, &user_model.User{Name: "user2"})
	session := loginUser(t, owner.Name)
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeAll)

	repoName := "migrated-from-mock-gitea"
	cloneAddr := mockServer.URL + "/gitea/test_repo.git"

	req := NewRequestWithJSON(t, "POST", "/api/v1/repos/migrate", &structs.MigrateRepoOptions{
		CloneAddr:    cloneAddr,
		RepoOwnerID:  owner.ID,
		RepoName:     repoName,
		Service:      structs.GiteaService.Name(),
		Wiki:         true,
		Milestones:   true,
		Labels:       true,
		Issues:       true,
		PullRequests: true,
		Releases:     true,
	}).AddTokenAuth(token)
	MakeRequest(t, req, http.StatusCreated)

	migratedRepo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{Name: repoName})
	assert.Equal(t, owner.ID, migratedRepo.OwnerID)
	assert.Equal(t, structs.GiteaService, migratedRepo.OriginalServiceType)
	assert.Equal(t, cloneAddr, migratedRepo.OriginalURL)

	issueCount := unittest.GetCount(t,
		&issues_model.Issue{RepoID: migratedRepo.ID},
		unittest.Cond("is_pull = ?", false),
	)
	assert.Equal(t, 7, issueCount)
	pullCount := unittest.GetCount(t,
		&issues_model.Issue{RepoID: migratedRepo.ID},
		unittest.Cond("is_pull = ?", true),
	)
	assert.Equal(t, 6, pullCount)

	issue4, err := issues_model.GetIssueWithAttrsByIndex(t.Context(), migratedRepo.ID, 4)
	require.NoError(t, err)
	assert.Equal(t, owner.ID, issue4.PosterID)
	assert.Equal(t, "Ghost", issue4.OriginalAuthor)
	assert.Equal(t, int64(-1), issue4.OriginalAuthorID)
	assert.Equal(t, "what is this repo about?", issue4.Title)
	assert.True(t, issue4.IsClosed)
	assert.True(t, issue4.IsLocked)
	if assert.NotNil(t, issue4.Milestone) {
		assert.Equal(t, "V1", issue4.Milestone.Name)
	}
	labelNames := make([]string, 0, len(issue4.Labels))
	for _, label := range issue4.Labels {
		labelNames = append(labelNames, label.Name)
	}
	assert.Contains(t, labelNames, "Question")
	reactionTypes := make([]string, 0, len(issue4.Reactions))
	for _, reaction := range issue4.Reactions {
		reactionTypes = append(reactionTypes, reaction.Type)
	}
	assert.ElementsMatch(t, []string{"laugh"}, reactionTypes) // gitea's author is ghost which will be ignored when migrating reactions

	comments, err := issues_model.FindComments(t.Context(), &issues_model.FindCommentsOptions{
		IssueID: issue4.ID,
		Type:    issues_model.CommentTypeComment,
	})
	require.NoError(t, err)
	require.Len(t, comments, 2)
	assert.Equal(t, owner.ID, comments[0].PosterID)
	assert.Equal(t, int64(689), comments[0].OriginalAuthorID)
	assert.Equal(t, "6543", comments[0].OriginalAuthor)
	assert.Contains(t, comments[0].Content, "TESTSET for gitea2gitea")
	assert.Equal(t, owner.ID, comments[1].PosterID)
	assert.Equal(t, "Ghost", comments[1].OriginalAuthor)
	assert.Equal(t, int64(-1), comments[1].OriginalAuthorID)
	assert.Equal(t, "Oh!", strings.TrimSpace(comments[1].Content))

	pr12, err := issues_model.GetPullRequestByIndex(t.Context(), migratedRepo.ID, 12)
	require.NoError(t, err)
	assert.Equal(t, owner.ID, pr12.Issue.PosterID)
	assert.Equal(t, "6543", pr12.Issue.OriginalAuthor)
	assert.Equal(t, int64(689), pr12.Issue.OriginalAuthorID)
	assert.Equal(t, "Dont Touch", pr12.Issue.Title)
	assert.True(t, pr12.Issue.IsClosed)
	assert.True(t, pr12.HasMerged)
	assert.Equal(t, "827aa28a907853e5ddfa40c8f9bc52471a2685fd", pr12.MergedCommitID)
	assert.NoError(t, pr12.Issue.LoadMilestone(t.Context()))
	if assert.NotNil(t, pr12.Issue.Milestone) {
		assert.Equal(t, "V2 Finalize", pr12.Issue.Milestone.Name)
	}
	assert.Contains(t, pr12.Issue.Content, "dont touch")

	pr8, err := issues_model.GetPullRequestByIndex(t.Context(), migratedRepo.ID, 8)
	require.NoError(t, err)
	assert.Equal(t, owner.ID, pr8.Issue.PosterID)
	assert.Equal(t, "6543", pr8.Issue.OriginalAuthor)
	assert.Equal(t, int64(689), pr8.Issue.OriginalAuthorID)
	assert.Equal(t, "add garbage for close pull", pr8.Issue.Title)
	assert.True(t, pr8.Issue.IsClosed)
	assert.False(t, pr8.HasMerged)
	assert.Contains(t, pr8.Issue.Content, "well you'll see")

	pr13, err := issues_model.GetPullRequestByIndex(t.Context(), migratedRepo.ID, 13)
	require.NoError(t, err)
	assert.Equal(t, owner.ID, pr13.Issue.PosterID)
	assert.Equal(t, "6543", pr13.Issue.OriginalAuthor)
	assert.Equal(t, int64(689), pr13.Issue.OriginalAuthorID)
	assert.Equal(t, "extend", pr13.Issue.Title)
	assert.False(t, pr13.Issue.IsClosed)
	assert.False(t, pr13.HasMerged)
	assert.True(t, pr13.Issue.IsLocked)

	gitRepo, err := gitrepo.OpenRepository(t.Context(), migratedRepo)
	require.NoError(t, err)
	defer gitRepo.Close()

	branches, _, err := gitRepo.GetBranchNames(0, 0)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"6543-patch-1", "master", "6543-forks/add-xkcd-2199"}, branches) // last branch comes from the pull request

	branchNames, err := git_model.FindBranchNames(t.Context(), git_model.FindBranchOptions{
		RepoID: migratedRepo.ID,
	})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"6543-patch-1", "master", "6543-forks/add-xkcd-2199"}, branchNames)

	tags, _, err := gitRepo.GetTagInfos(0, 0)
	require.NoError(t, err)
	tagNames := make([]string, 0, len(tags))
	for _, tag := range tags {
		tagNames = append(tagNames, tag.Name)
	}
	assert.ElementsMatch(t, []string{"V1", "v2-rc1"}, tagNames)

	releases, err := db.Find[repo_model.Release](t.Context(), repo_model.FindReleasesOptions{
		RepoID:        migratedRepo.ID,
		IncludeDrafts: true,
		IncludeTags:   false,
	})
	require.NoError(t, err)
	require.Len(t, releases, 2)

	releaseMap := make(map[string]*repo_model.Release, len(releases))
	for _, rel := range releases {
		releaseMap[rel.TagName] = rel
		assert.Equal(t, owner.ID, rel.PublisherID)
		assert.Equal(t, "6543", rel.OriginalAuthor)
		assert.Equal(t, int64(689), rel.OriginalAuthorID)
		assert.False(t, rel.IsDraft)
	}

	require.Contains(t, releaseMap, "v2-rc1")
	v2Release := releaseMap["v2-rc1"]
	assert.Equal(t, "Second Release", v2Release.Title)
	assert.True(t, v2Release.IsPrerelease)
	assert.Contains(t, v2Release.Note, "this repo has:")

	require.Contains(t, releaseMap, "V1")
	v1Release := releaseMap["V1"]
	assert.Equal(t, "First Release", v1Release.Title)
	assert.False(t, v1Release.IsPrerelease)
	assert.Equal(t, "as title", strings.TrimSpace(v1Release.Note))
}

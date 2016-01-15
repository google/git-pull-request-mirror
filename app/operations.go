/*
Copyright 2015 Google Inc. All Rights Reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/go-github/github"
	"golang.org/x/net/context"
	"golang.org/x/oauth2"
	"google.golang.org/appengine"
	"google.golang.org/appengine/log"

	"github-mirror/mirror"
)

const (
	maxRetries   = 200
	scopesHeader = "X-OAuth-Scopes"
	secretSize   = 64
)

var (
	hookType = "web"
)

var errTooManyRetries = errors.New("Too many retries!")

// retry reduces github api-retrying boilerplate for when we run out of requests.
// It will call the given function until it succeeds or errors out, or until it
// has retried more than $maxRetries times.
// Use like so:
//
//	var zen string
//
//	err = retry(c, func() (resp *github.Response, err error) {
//		zen, resp, err = githubClient.Zen()
//		return
//	})
//
func retry(c context.Context, f func() (*github.Response, error)) error {
	for i := 0; i < maxRetries; i++ {
		resp, err := f()

		if resp != nil && resp.Rate.Remaining == 0 {
			// Timeout problems
			waitDuration := resp.Reset.Sub(time.Now())
			log.Infof(c, "Ran out of github API requests; sleeping %v (until %v)",
				waitDuration,
				resp.Reset.Time)
			time.Sleep(waitDuration)
			continue
		}
		if err != nil {
			// Error unrelated to timeout
			return err
		}
		// operation performed successfully
		return nil
	}
	log.Errorf(c, "Too many retries, abandoning operation")
	return errTooManyRetries
}

// Each repository goes through the following lifecycle states:
//
//   [validating]
//     |
//     | (validate access to the repo)
//     |
//     V
//   [hooks initializing]
//     |
//     | (create the web hook, and then recieve the web hook "ping")
//     |
//     V
//   [initializing]
//     |
//     | (mirror the pull requests)
//     |
//     V
//   [ready]
//     | ^
//     | | (recieve any web hook and mirror the pull requests)
//     | |
//     +-+

// validate ensures that the repo is accessible
func validate(user, repo string) {
	c, done := context.WithCancel(appengine.BackgroundContext())
	defer done()

	log.Infof(c, "Validating repo %s/%s", user, repo)

	errorf := makeErrorf(c, user, repo)

	repoData, err := getRepoData(c, user, repo)
	if err != nil {
		errorf("Can't load repo to validate: %s", err.Error())
		return
	}

	httpClient := oauth2.NewClient(c, oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: repoData.Token},
	))

	githubClient := github.NewClient(httpClient)

	var resp *github.Response
	err = retry(c, func() (*github.Response, error) {
		// APIMeta will always succeed and will tell us what scopes
		// we have.
		_, resp, err = githubClient.APIMeta()
		return resp, err
	})

	if err != nil {
		errorf("Can't validate repo %s/%s: %s", user, repo, err.Error())
		return
	}

	scopesHeader := resp.Header["X-Oauth-Scopes"]

	if len(scopesHeader) == 0 {
		// No scopes means that a token has access to all *public* repositories.
		// It's simplest to just require private access.
		errorf("Invalid token, missing scopes: `repo`, `write:repo_hook`")
		return
	}

	// The token has scopes.
	// Let's make sure it has all the ones we need enabled.
	// Note that strictly speaking, we need the repo, public_repo,
	// write:repo_hook, and repo:status scopes, but repo and
	// write:repo_hook subsume the others.

	// Necessary because github makes things comma-delimited instead
	// of semicolon-delimited for some reason.
	scopes := strings.Split(scopesHeader[0], ", ")

	var hasRepo bool
	var hasWriteRepoHook bool
	for _, scope := range scopes {
		switch scope {
		case "repo":
			hasRepo = true
		case "write:repo_hook":
			hasWriteRepoHook = true
		}
	}

	if !hasRepo || !hasWriteRepoHook {
		var missingScopes string
		if !hasRepo && !hasWriteRepoHook {
			missingScopes = "repo, write:repo_hook"
		} else if !hasRepo {
			missingScopes = "repo"
		} else {
			missingScopes = "write:repo_hook"
		}
		errorf("Invalid token for %s/%s, missing scopes: %s",
			user,
			repo,
			missingScopes,
		)
		return
	}

	log.Infof(c, "Validated repo %s/%s", user, repo)

	var remoteRepo *github.Repository
	err = retry(c, func() (resp *github.Response, err error) {
		remoteRepo, resp, err = githubClient.Repositories.Get(user, repo)
		return
	})

	if err != nil {
		errorf("Can't validate repo %s/%s: %s", user, repo, err.Error())
	}

	err = modifyRepoData(c, user, repo, func(item *repoStorageData) {
		item.Status = statusHooksInitializing
	})

	if err != nil {
		errorf("Can't change repo status: %s", err.Error())
	}

	go createHooks(user, repo)
}

// initialize performs initial reading and commiting for the repository
func initialize(userName, repoName string) {
	c, done := context.WithCancel(appengine.BackgroundContext())
	defer done()

	errorf := makeErrorf(c, userName, repoName)
	repoData, err := getRepoData(c, userName, repoName)
	if err != nil {
		errorf("Can't load repo to initialize: %s", err.Error())
		return
	}

	repo, err := clone(c, userName, repoName, userName, repoData.Token)
	if err != nil {
		errorf("Can't clone repo: %s", err.Error())
		return
	}

	client := github.NewClient(oauth2.NewClient(c, oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: repoData.Token},
	)))

	errChan := make(chan error, 1000)
	nErrors := 0
	go func() {
		for err := range errChan {
			errorf(err.Error())
			nErrors++
		}
	}()

	reviews, err := mirror.GetAllPullRequests(repo, userName, repoName, client, errChan)
	if err != nil {
		errorf("Can't get PRs: %s", err.Error())
		return
	}

	statuses, err := mirror.GetAllStatuses(userName, repoName, client, errChan)
	if err != nil {
		errorf("Can't get statuses: %s", err.Error())
		return
	}
	close(errChan)

	nStatuses := len(statuses)
	nReviews := len(reviews)
	logChan := make(chan string, 1000)
	go func() {
		for msg := range logChan {
			log.Infof(c, msg)
		}
	}()
	log.Infof(c, "Done reading! Read %d statuses, %d PRs", nStatuses, nReviews)
	log.Infof(c, "Committing...\n")
	if err := mirror.WriteNewReports(statuses, repo, logChan); err != nil {
		errorf(err.Error())
		return
	}
	if err := mirror.WriteNewReviews(reviews, repo, logChan); err != nil {
		errorf(err.Error())
		return
	}
	close(logChan)
	err = syncNotes(repo)
	if err != nil {
		errorf("Error pushing initialization changes for %s/%s: %s",
			userName,
			repoName,
			err.Error())
		return
	}
	log.Infof(c, "Success initializing %s/%s", userName, repoName)

	err = modifyRepoData(c, userName, repoName, func(item *repoStorageData) {
		item.Status = statusReady
	})

	if err != nil {
		errorf("Can't change repo status for %s/%s: %s",
			userName,
			repoName,
			err.Error(),
		)
	}
}

// hook sets up webhooks for a given repository
func createHooks(userName, repoName string) {
	c, done := context.WithCancel(appengine.BackgroundContext())
	defer done()

	errorf := makeErrorf(c, userName, repoName)
	repoData, err := getRepoData(c, userName, repoName)
	if err != nil {
		errorf("Can't load repo to hook: %s", err.Error())
		return
	}

	client := github.NewClient(oauth2.NewClient(c, oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: repoData.Token},
	)))

	active := true

	secret := make([]byte, secretSize)
	_, err = rand.Read(secret)
	if err != nil {
		errorf("Can't create secret key: %s", err.Error())
		return
	}
	secretHex := hex.EncodeToString(secret)

	// TODO allow non-appspot urls?
	url := fmt.Sprintf("https://github-mirror-dot-%s.appspot.com/hook/%s/%s", appengine.AppID(c), userName, repoName)

	log.Infof(c, "Creating hook for %s/%s: url `%s`", userName, repoName, url)

	var hook *github.Hook
	err = retry(c, func() (resp *github.Response, err error) {
		hook, resp, err = client.Repositories.CreateHook(userName, repoName, &github.Hook{
			Name: &hookType,
			Events: []string{
				eventPing,
				eventStatus,
				eventPullRequest,
				eventDiffComment,
				eventIssueComment,
			},
			Active: &active,
			Config: map[string]interface{}{
				"url":          url,
				"content_type": "json",
				"secret":       secretHex,
				"insecure_ssl": false,
			},
		})
		return
	})
	if err != nil {
		errorf("Can't create hook: %s", err.Error())
		return
	}

	if hook.ID == nil {
		errorf("No hook ID for new hook")
		return
	}

	log.Infof(c, "Hook creation for %s/%s successful", userName, repoName)

	err = modifyRepoData(c, userName, repoName, func(item *repoStorageData) {
		item.HookSecret = secretHex
		item.HookID = *hook.ID
	})

	if err != nil {
		errorf("Can't set repo status to ready: %s", err.Error())
		return
	}

	log.Infof(c, "Repo waiting for hook ping: %s/%s", userName, repoName)
}

// deactivate deletes webhooks and forgets data for a given repository
func deactivate(userName, repoName string) {
	c, done := context.WithCancel(appengine.BackgroundContext())
	defer done()

	errorf := makeErrorf(c, userName, repoName)

	repoData, err := getRepoData(c, userName, repoName)
	if err != nil {
		errorf("Can't load repo to deactivate: %s", err.Error())
		return
	}

	client := github.NewClient(oauth2.NewClient(c, oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: repoData.Token},
	)))

	log.Infof(c, "Deleting hook for repository %s/%s", userName, repoName)
	err = retry(c, func() (resp *github.Response, err error) {
		resp, err = client.Repositories.DeleteHook(userName, repoName, repoData.HookID)
		return
	})
	if err != nil {
		errorf("Can't delete webhook: %s", err.Error())
		// Keep going; we should still delete the repository data
	} else {
		log.Infof(c, "Deleting hook for repository %s/%s succeeded")
	}

	log.Infof(c, "Deleting repository data for %s/%s", userName, repoName)
	err = deleteRepoData(c, userName, repoName)
	if err != nil {
		errorf("Can't delete repository data: %s", err.Error())
		return
	}
}

// restartAbandonedOperations runs when the web server starts.
// It goes through the repos in the data store and checks their statuses.
// If they're validating or initializing, those processes will restart.
// If they actually finished validating / initializing but didn't write
// to the store that's fine, since all operations are indempotent; we
// can redo it.
func restartAbandonedOperations() {
	c, done := context.WithCancel(appengine.BackgroundContext())
	defer done()

	log.Infof(c, "Restarting abandoned operations...")

	repos, err := getAllRepoData(c)
	if err != nil {
		log.Errorf(c, "Can't load repos: %s", err.Error)
		return
	}

	for _, repo := range repos {
		switch repo.Status {
		case statusReady:
			log.Infof(c, "Repo ready: %s/%s", repo.User, repo.Repo)
		case statusError:
			log.Infof(c, "Repo errored out: %s/%s", repo.User, repo.Repo)
		case statusValidating:
			log.Infof(c, "Repo requires validation: %s/%s", repo.User, repo.Repo)
			go validate(repo.User, repo.Repo)
		case statusInitializing:
			log.Infof(c, "Repo requires initialization: %s/%s", repo.User, repo.Repo)
			go initialize(repo.User, repo.Repo)
		case statusHooksInitializing:
			log.Infof(c, "Repo requires hook initialization: %s/%s", repo.User, repo.Repo)
			go createHooks(repo.User, repo.Repo)
		default:
			log.Errorf(c, "Unrecognized status for repo %s/%s: %s", repo.User, repo.Repo, repo.Status)
		}
	}
}

// Webhook operations

// All webhooks are sent a "ping" event on creation
func pingHook(userName, repoName string, repoData repoStorageData, content []byte) {
	c, done := context.WithCancel(appengine.BackgroundContext())
	defer done()

	errorf := makeErrorf(c, userName, repoName)

	var payload struct {
		Zen    string `json:"zen"`
		HookID int    `json:"hook_id"`
	}

	err := json.Unmarshal(content, &payload)
	if err != nil {
		errorf("Can't parse payload for ping hook: %s, %s", err.Error(), content)
		return
	}

	err = modifyRepoData(c, userName, repoName, func(item *repoStorageData) {
		item.Status = statusInitializing
	})

	if err != nil {
		log.Errorf(c, "Can't set repo %s/%s to initializing: %s", userName, repoName, err.Error())
	}

	// Pass of to initialization
	go initialize(userName, repoName)
}

// makeErrorf returns a utility function that logs a given error and then sets the repo's error information to that error
func makeErrorf(c context.Context, userName, repoName string) func(string, ...interface{}) {
	return func(format string, params ...interface{}) {
		errText := fmt.Sprintf(format, params...)
		log.Errorf(c, "%s/%s: %s", userName, repoName, errText)
		err := setRepoError(c, userName, repoName, errText)
		if err != nil {
			log.Errorf(c, "Can't set repo error status for %s/%s: %s",
				userName,
				repoName,
				err.Error(),
			)
		}
	}
}

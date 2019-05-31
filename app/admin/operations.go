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
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/go-github/github"
	"golang.org/x/oauth2"
	"google.golang.org/appengine"
	"google.golang.org/appengine/log"
)

const (
	maxRetries   = 200
	scopesHeader = "X-OAuth-Scopes"
	secretSize   = 64

	githubEventHeader     = "X-Github-Event"
	githubSignatureHeader = "X-Hub-Signature"

	eventPing         = "ping"
	eventStatus       = "status"
	eventPullRequest  = "pull_request"
	eventDiffComment  = "pull_request_review_comment"
	eventIssueComment = "issue_comment"
)

var errTooManyRetries = errors.New("Too many retries!")

// retry reduces github api-retrying boilerplate for when we run out of requests.
// It will call the given function until it succeeds or errors out, or until it
// has retried more than $maxRetries times.
// Use like so:
//
//	var zen string
//
//	err = retry(ctx, func() (resp *github.Response, err error) {
//		zen, resp, err = githubClient.Zen()
//		return
//	})
//
func retry(ctx context.Context, f func() (*github.Response, error)) error {
	for i := 0; i < maxRetries; i++ {
		resp, err := f()

		if resp != nil && resp.Rate.Remaining == 0 {
			// Timeout problems
			waitDuration := resp.Rate.Reset.Sub(time.Now())
			log.Infof(ctx, "Ran out of github API requests; sleeping %v (until %v)",
				waitDuration,
				resp.Rate.Reset.Time)
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
	log.Errorf(ctx, "Too many retries, abandoning operation")
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
//     | (create the web hook, and then receive the web hook "ping")
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
func validate(ctx context.Context, user, repo string) {
	log.Infof(ctx, "Validating repo %s/%s", user, repo)

	errorf := makeErrorf(ctx, user, repo)

	repoData, err := getRepoData(ctx, user, repo)
	if err != nil {
		errorf("Can't load repo to validate: %s", err.Error())
		return
	}

	httpClient := oauth2.NewClient(ctx, oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: repoData.Token},
	))

	githubClient := github.NewClient(httpClient)

	var resp *github.Response
	err = retry(ctx, func() (*github.Response, error) {
		// APIMeta will always succeed and will tell us what scopes
		// we have.
		_, resp, err = githubClient.APIMeta(ctx)
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
		case "admin:repo_hook":
			hasWriteRepoHook = true
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
		errorf("Invalid token for %s/%s, missing scopes: %s... had: %v",
			user,
			repo,
			missingScopes,
			scopes)
		return
	}

	log.Infof(ctx, "Validated repo %s/%s", user, repo)

	err = retry(ctx, func() (resp *github.Response, err error) {
		_, resp, err = githubClient.Repositories.Get(ctx, user, repo)
		return
	})

	if err != nil {
		errorf("Can't validate repo %s/%s: %s", user, repo, err.Error())
	}

	err = modifyRepoData(ctx, user, repo, func(item *repoStorageData) {
		item.Status = statusHooksInitializing
	})

	if err != nil {
		errorf("Can't change repo status: %s", err.Error())
	}

	createHooks(ctx, user, repo)
}

// hook sets up webhooks for a given repository
func createHooks(ctx context.Context, userName, repoName string) {
	errorf := makeErrorf(ctx, userName, repoName)
	repoData, err := getRepoData(ctx, userName, repoName)
	if err != nil {
		errorf("Can't load repo to hook: %s", err.Error())
		return
	}

	client := github.NewClient(oauth2.NewClient(ctx, oauth2.StaticTokenSource(
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
	url := fmt.Sprintf("https://github-mirror-dot-%s.appspot.com/hook/%s/%s", appengine.AppID(ctx), userName, repoName)

	log.Infof(ctx, "Creating hook for %s/%s: url `%s`", userName, repoName, url)

	var hook *github.Hook
	err = retry(ctx, func() (resp *github.Response, err error) {
		hook, resp, err = client.Repositories.CreateHook(ctx, userName, repoName, &github.Hook{
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

	log.Infof(ctx, "Hook creation for %s/%s successful", userName, repoName)

	err = modifyRepoData(ctx, userName, repoName, func(item *repoStorageData) {
		item.HookSecret = secretHex
		item.HookID = *hook.ID
	})

	if err != nil {
		errorf("Can't set repo status to ready: %s", err.Error())
		return
	}

	log.Infof(ctx, "Repo waiting for hook ping: %s/%s", userName, repoName)
}

// deactivate deletes webhooks and forgets data for a given repository
func deactivate(ctx context.Context, userName, repoName string) {
	errorf := makeErrorf(ctx, userName, repoName)

	repoData, err := getRepoData(ctx, userName, repoName)
	if err != nil {
		errorf("Can't load repo to deactivate: %s", err.Error())
		return
	}

	client := github.NewClient(oauth2.NewClient(ctx, oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: repoData.Token},
	)))

	log.Infof(ctx, "Deleting hook for repository %s/%s", userName, repoName)
	err = retry(ctx, func() (resp *github.Response, err error) {
		resp, err = client.Repositories.DeleteHook(ctx, userName, repoName, repoData.HookID)
		return
	})
	if err != nil {
		errorf("Can't delete webhook: %s", err.Error())
		// Keep going; we should still delete the repository data
	} else {
		log.Infof(ctx, "Deleting hook for repository %s/%s succeeded", userName, repoName)
	}

	log.Infof(ctx, "Deleting repository data for %s/%s", userName, repoName)
	err = deleteRepoData(ctx, userName, repoName)
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
func restartAbandonedOperations(ctx context.Context) {
	ctx, done := context.WithCancel(ctx)
	defer done()

	log.Infof(ctx, "Restarting abandoned operations...")

	repos, err := getAllRepoData(ctx)
	if err != nil {
		log.Errorf(ctx, "Can't load repos: %s", err.Error())
		return
	}

	var wg sync.WaitGroup
	for _, repo := range repos {
		wg.Add(1)
		go func(repo repoStorageData) {
			switch repo.Status {
			case statusReady:
				log.Infof(ctx, "Repo ready: %s/%s", repo.User, repo.Repo)
			case statusError:
				log.Infof(ctx, "Repo errored out: %s/%s", repo.User, repo.Repo)
			case statusValidating:
				log.Infof(ctx, "Repo requires validation: %s/%s", repo.User, repo.Repo)
				validate(ctx, repo.User, repo.Repo)
			case statusInitializing:
				log.Infof(ctx, "Repo requires initialization: %s/%s", repo.User, repo.Repo)
			case statusHooksInitializing:
				log.Infof(ctx, "Repo requires hook initialization: %s/%s", repo.User, repo.Repo)
				createHooks(ctx, repo.User, repo.Repo)
			default:
				log.Errorf(ctx, "Unrecognized status for repo %s/%s: %s", repo.User, repo.Repo, repo.Status)
			}
			wg.Done()
		}(repo)
	}
	wg.Wait()
}

// makeErrorf returns a utility function that logs a given error and then sets the repo's error information to that error
func makeErrorf(ctx context.Context, userName, repoName string) func(string, ...interface{}) {
	return func(format string, params ...interface{}) {
		errText := fmt.Sprintf(format, params...)
		log.Errorf(ctx, "%s/%s: %s", userName, repoName, errText)
		err := setRepoError(ctx, userName, repoName, errText)
		if err != nil {
			log.Errorf(ctx, "Can't set repo error status for %s/%s: %s",
				userName,
				repoName,
				err.Error(),
			)
		}
	}
}

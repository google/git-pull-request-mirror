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
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"strings"

	"cloud.google.com/go/datastore"
	"github.com/google/git-pull-request-mirror/mirror"
	"github.com/google/go-github/github"
	"golang.org/x/oauth2"
	"google.golang.org/appengine"

	"cloud.google.com/go/compute/metadata"
)

const (
	githubEventHeader     = "X-Github-Event"
	githubSignatureHeader = "X-Hub-Signature"

	eventPing         = "ping"
	eventStatus       = "status"
	eventPullRequest  = "pull_request"
	eventDiffComment  = "pull_request_review_comment"
	eventIssueComment = "issue_comment"
)

// makeErrorf returns a utility function that logs a given error and then sets the repo's error information to that error
func makeErrorf(ctx context.Context, c *datastore.Client, userName, repoName string) func(string, ...interface{}) {
	return func(format string, params ...interface{}) {
		errText := fmt.Sprintf(format, params...)
		log.Printf("%s/%s: %s", userName, repoName, errText)
		err := setRepoError(ctx, c, userName, repoName, errText)
		if err != nil {
			log.Printf("Can't set repo error status for %s/%s: %s",
				userName,
				repoName,
				err.Error(),
			)
		}
	}
}

// initialize performs initial reading and commiting for the repository
func initialize(ctx context.Context, c *datastore.Client, userName, repoName string) {
	errorf := makeErrorf(ctx, c, userName, repoName)
	repoData, err := getRepoData(ctx, c, userName, repoName)
	if err != nil {
		errorf("Can't load repo to initialize: %s", err.Error())
		return
	}

	repo, err := clone(ctx, userName, repoName, repoData.Token)
	if err != nil {
		errorf("Can't clone repo: %v", err)
		return
	}

	client := github.NewClient(oauth2.NewClient(ctx, oauth2.StaticTokenSource(
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
			log.Printf(msg)
		}
	}()
	log.Printf("Done reading! Read %d statuses, %d PRs", nStatuses, nReviews)
	log.Printf("Committing...\n")
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
	log.Printf("Success initializing %s/%s", userName, repoName)

	err = modifyRepoData(ctx, c, userName, repoName, func(item *repoStorageData) {
		item.Status = statusReady
		item.ErrorCause = ""
	})

	if err != nil {
		errorf("Can't change repo status for %s/%s: %s",
			userName,
			repoName,
			err.Error(),
		)
	}
}

// All webhooks are sent a "ping" event on creation
func pingHook(ctx context.Context, c *datastore.Client, userName, repoName string, repoData repoStorageData, content []byte) {
	var payload struct {
		Zen    string `json:"zen"`
		HookID int    `json:"hook_id"`
	}

	err := json.Unmarshal(content, &payload)
	if err != nil {
		log.Printf("Can't parse payload for ping hook: %s, %s", err.Error(), content)
		return
	}

	err = modifyRepoData(ctx, c, userName, repoName, func(item *repoStorageData) {
		item.Status = statusInitializing
	})

	if err != nil {
		log.Printf("Can't set repo %s/%s to initializing: %s", userName, repoName, err.Error())
	}

	// Pass off to initialization
	initialize(ctx, c, userName, repoName)
}

type hookHandler struct {
	projectID string
}

func (h *hookHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()

	sigHex := req.Header.Get(githubSignatureHeader)
	if !strings.HasPrefix(sigHex, "sha1=") || strings.TrimPrefix(sigHex, "sha1=") == "" {
		log.Printf("Hook hit with no signature")
		http.Error(w, "Webhook requires "+githubSignatureHeader+" header", http.StatusBadRequest)
		return
	}

	sig, err := hex.DecodeString(strings.TrimPrefix(sigHex, "sha1="))
	if err != nil {
		log.Printf("Hook can't decode hex signature `%s`: %s", sigHex, err.Error())
		http.Error(w, "Can't decode signature", http.StatusBadRequest)
		return
	}

	content, err := ioutil.ReadAll(req.Body)
	if err != nil {
		log.Printf("Hook request error: %s", err.Error())
		http.Error(w, "Can't read request body", http.StatusInternalServerError)
		return
	}

	event := req.Header.Get(githubEventHeader)
	if event == "" {
		log.Printf("Hook hit with no event type")
		http.Error(w, "Webhook requires "+githubEventHeader+" header", http.StatusBadRequest)
		return
	}

	pathParts := strings.Split(req.URL.Path, "/")
	if len(pathParts) != 4 {
		log.Printf("Hook hit with invalid path length: %d", len(pathParts))
		http.Error(w, "Invalid /hook/:user/:repo URL", http.StatusBadRequest)
		return
	}

	userName := pathParts[2]
	repoName := pathParts[3]

	c, err := datastore.NewClient(ctx, h.projectID)
	if err != nil {
		log.Printf("Hook cannot connect to the datastore: %v", err)
		http.Error(w, "Cannot connect to the datastore", http.StatusInternalServerError)
		return
	}

	repo, err := getRepoData(ctx, c, userName, repoName)
	if err != nil {
		log.Printf("Hook can't retrieve repo: %s", err.Error())
		http.Error(w, "Can't retrieve repo information", http.StatusInternalServerError)
		return
	}

	mac := hmac.New(sha1.New, []byte(repo.HookSecret))
	mac.Write(content)
	expectedSig := mac.Sum(nil)
	if !bytes.Equal(expectedSig, sig) {
		log.Printf("Hook hit with invalid signature; '%x' vs. '%x'", expectedSig, sig)
		http.Error(w, "Invalid signature", http.StatusBadRequest)
		return
	}

	go func() {
		ctx, done := context.WithCancel(context.Background())
		defer done()

		if event == eventPing {
			pingHook(ctx, c, userName, repoName, repo, content)
			return
		}
		initialize(ctx, c, userName, repoName)
	}()
	w.WriteHeader(http.StatusOK)
}

func main() {
	projectID, err := metadata.ProjectID()
	if err != nil {
		log.Fatalf("Failed to read the project ID from the metadata server: %v", err)
	}

	http.Handle("/hook/", &hookHandler{
		projectID: projectID,
	})

	appengine.Main()
}

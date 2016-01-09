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
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"io/ioutil"
	"net/http"
	"strings"

	"google.golang.org/appengine"
	"google.golang.org/appengine/log"
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

func setupHookHandlers() {
	http.HandleFunc("/hook/", hookHandler)
}

func hookHandler(w http.ResponseWriter, req *http.Request) {
	c := appengine.NewContext(req)

	sigHex := req.Header.Get(githubSignatureHeader)
	if !strings.HasPrefix(sigHex, "sha1=") || strings.TrimPrefix(sigHex, "sha1=") == "" {
		log.Errorf(c, "Hook hit with no signature")
		http.Error(w, "Webhook requires "+githubSignatureHeader+" header", http.StatusBadRequest)
		return
	}

	sig, err := hex.DecodeString(strings.TrimPrefix(sigHex, "sha1="))
	if err != nil {
		log.Errorf(c, "Hook can't decode hex signature `%s`: %s", sigHex, err.Error())
		http.Error(w, "Can't decode signature", http.StatusBadRequest)
		return
	}

	content, err := ioutil.ReadAll(req.Body)
	if err != nil {
		log.Errorf(c, "Hook request error: %s", err.Error())
		http.Error(w, "Can't read request body", http.StatusInternalServerError)
		return
	}

	event := req.Header.Get(githubEventHeader)
	if event == "" {
		log.Errorf(c, "Hook hit with no event type")
		http.Error(w, "Webhook requires "+githubEventHeader+" header", http.StatusBadRequest)
		return
	}

	pathParts := strings.Split(req.URL.Path, "/")
	if len(pathParts) != 4 {
		log.Errorf(c, "Hook hit with invalid path length: %d", len(pathParts))
		http.Error(w, "Invalid /hook/:user/:repo URL", http.StatusBadRequest)
		return
	}

	userName := pathParts[2]
	repoName := pathParts[3]

	repo, err := getRepoData(c, userName, repoName)
	if err != nil {
		log.Errorf(c, "Hook can't retrieve repo: %s", err.Error())
		http.Error(w, "Can't retrieve repo information", http.StatusInternalServerError)
		return
	}

	mac := hmac.New(sha1.New, []byte(repo.HookSecret))
	mac.Write(content)
	expectedSig := mac.Sum(nil)
	if !bytes.Equal(expectedSig, sig) {
		log.Errorf(c, "Hook hit with invalid signature; '%x' vs. '%x'", expectedSig, sig)
		http.Error(w, "Invalid signature", http.StatusBadRequest)
		return
	}

	if event == eventPing {
		go pingHook(userName, repoName, repo, content)
		w.WriteHeader(http.StatusOK)
		return
	}

	go initialize(userName, repoName)
	w.WriteHeader(http.StatusOK)
}

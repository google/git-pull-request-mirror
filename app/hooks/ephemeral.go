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
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"

	"github.com/google/git-appraise/repository"
	"golang.org/x/net/context"
)

const (
	remoteName      = "origin"
	notesRefPattern = "refs/notes/devtools/*"
	fetchSpec       = "+refs/pull/*:refs/pull/*"
	retryAttempts   = 10
)

// Clone creates a local copy of the repository accessible at
// github.com/user/repo with token, in a system temp directory.
func clone(c context.Context, repoOwner, repoName, token string) (repository.Repo, error) {
	dir, err := ioutil.TempDir("", fmt.Sprintf("%s-%s", repoOwner, repoName))
	if err != nil {
		return nil, fmt.Errorf("failure creating the temporary directory for cloning: %v", err)
	}
	cloneCmd := exec.Command("git", "clone", makeRemoteURL(token, repoOwner, repoName), dir)
	if out, err := cloneCmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("failure issuing the clone command, %v: %q", err, out)
	}
	repo, err := repository.NewGitRepo(dir)
	if err != nil {
		return nil, fmt.Errorf("failure loading the cloned repository: %v", err)
	}
	if err := repo.PullNotes(remoteName, notesRefPattern); err != nil {
		return nil, fmt.Errorf("failure pulling the git-notes: %v", err)
	}
	fetchCmd := exec.Command("git", "fetch", "origin", fetchSpec)
	fetchCmd.Dir = dir
	if _, err := fetchCmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("failure fetching pull requests from the remote: %v", err)
	}
	configUserCmd := exec.Command("git", "config", "--local", "--add", "user.name", "Github Mirror")
	configUserCmd.Dir = dir
	if _, err := configUserCmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("failure configuring the local git user: %v", err)
	}
	userEmail := os.Getenv("GOOGLE_CLOUD_PROJECT") + "@appspot.gserviceaccount.com"
	configEmailCmd := exec.Command("git", "config", "--local", "--add", "user.email", userEmail)
	configEmailCmd.Dir = dir
	if out, err := configEmailCmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("failure configuring the local get user email address: %v, %q", err, out)
	}
	return repo, nil
}

func syncNotes(repo repository.Repo) error {
	var err error
	for attempt := 0; attempt < retryAttempts; attempt++ {
		err = repo.PullNotes(remoteName, notesRefPattern)
		if err == nil {
			err = repo.PushNotes(remoteName, notesRefPattern)
			if err == nil {
				return err
			}
		}
	}
	return err
}

// makeRemoteURL computes a URL to use with git
func makeRemoteURL(token, repoOwner, repo string) string {
	return fmt.Sprintf("https://%s@github.com/%s/%s", token, repoOwner, repo)
}
